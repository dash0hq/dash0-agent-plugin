// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Dash0 session extension for the GitHub Copilot app.
//
// The app runs one copy of this file per session (and again after /clear). It
// buffers the session events a turn produces and hands them to the
// copilot-app-on-event binary, through the bootstrap next to this file, which
// builds the spans and exports them over OTLP:
//
//   session events -> extension.mjs -> copilot-app-on-event.sh <event> -> binary -> OTLP
//
// Four events go to the binary: sessionStart, userPromptSubmitted, turnEnd (on
// session.idle, with the turn's buffered events) and sessionEnd. Everything is
// fail-open: an error is logged to stderr and the session carries on. stdout
// belongs to the SDK's JSON-RPC channel, so nothing here writes to it.

import { joinSession } from "@github/copilot-sdk/extension";
import { spawn } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const IS_WINDOWS = process.platform === "win32";
const BOOTSTRAP = join(HERE, IS_WINDOWS ? "copilot-app-on-event.ps1" : "copilot-app-on-event.sh");
// A full path, because Windows looks for a bare name in the cwd (the user's
// project) before PATH.
const POWERSHELL = join(process.env.SystemRoot ?? "C:\\Windows", "System32", "WindowsPowerShell", "v1.0", "powershell.exe");
// A hung bootstrap would stall every later event on the chain. Generous, since
// the first run downloads the binary.
const SPAWN_TIMEOUT_MS = 120_000;
// How long the sessionEnd hook waits for the final sends, under the 5s the app
// allows between SIGTERM and SIGKILL.
const EXIT_BUDGET_MS = 4_000;

// The session events the Go adapter reads, and the data keys it reads from
// them. Dropping the rest keeps the stdin payload small and keeps fields a
// later SDK adds from reaching the binary unannounced.
const KEEP = {
  "assistant.usage": ["model", "isAuto", "inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "reasoningTokens", "parentToolCallId"],
  "assistant.message": ["content", "parentToolCallId"],
  "tool.execution_start": ["toolCallId", "toolName", "arguments", "mcpServerName", "mcpToolName", "parentToolCallId"],
  "tool.execution_complete": ["toolCallId", "success", "result", "error", "parentToolCallId"],
  "subagent.started": ["toolCallId", "agentName", "agentDisplayName", "model"],
  "subagent.completed": ["toolCallId", "agentName", "model"],
  "subagent.failed": ["toolCallId", "agentName", "error", "model"],
  "session.error": ["errorType", "message"],
  abort: ["reason"],
};
// The binary truncates attributes at 16 KB; this only bounds memory and stdin.
const MAX_STRING = 32 * 1024;
// A runaway turn must not grow the buffer without limit.
const MAX_EVENTS = 5000;

const warn = (msg) => {
  try {
    process.stderr.write(`dash0: ${msg}\n`);
  } catch {}
};

const clip = (s) => (typeof s === "string" && s.length > MAX_STRING ? s.slice(0, MAX_STRING) : s);

function slim(event) {
  const keys = KEEP[event.type];
  const data = {};
  for (const k of keys) {
    let v = event.data?.[k];
    if (v === undefined || v === null) continue;
    if (k === "result") v = { content: clip(v.content) };
    else if (k === "error") v = typeof v === "string" ? { message: clip(v) } : { message: clip(v.message) };
    else v = clip(v);
    data[k] = v;
  }
  const out = { type: event.type, timestamp: event.timestamp, data };
  if (event.agentId) out.agentId = event.agentId;
  return out;
}

let session;
let chain = Promise.resolve();
let buffer = [];
// Whether a prompt opened the current turn. Events seen before the first prompt
// (the extension joined mid-turn) have no chat span to hang off.
let turnOpen = false;
// The prompt that opened the turn, and the messages the user steered it with.
// Steering joins the running turn instead of starting one, so its text goes out
// with turnEnd as part of the turn's input.
let turnPrompt = "";
let steered = [];
let started = false;
let ended = false;

// Runs the bootstrap with payload on stdin. Calls are chained so the binary
// sees a turn's prompt before its end, as the pipeline's trace context needs.
function send(eventName, payload, { surface = false } = {}) {
  const run = () =>
    new Promise((resolve) => {
      try {
        const [cmd, args] = IS_WINDOWS
          ? [POWERSHELL, ["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", BOOTSTRAP, eventName]]
          : ["bash", [BOOTSTRAP, eventName]];
        const child = spawn(cmd, args, {
          cwd: process.cwd(),
          stdio: ["pipe", "ignore", "pipe"],
          windowsHide: true,
          timeout: SPAWN_TIMEOUT_MS,
          killSignal: "SIGKILL",
        });
        let stderr = "";
        child.stderr.on("data", (d) => (stderr += d));
        child.on("error", (err) => {
          warn(`could not run ${BOOTSTRAP}: ${err.message}`);
          resolve();
        });
        child.on("close", () => {
          for (const line of stderr.split(/\r?\n/)) {
            if (!line.trim()) continue;
            if (surface && line.startsWith("dash0:")) session?.log(line).catch(() => {});
            else warn(line);
          }
          resolve();
        });
        child.stdin.on("error", () => {});
        child.stdin.end(JSON.stringify({ sessionId: session.sessionId, cwd: process.cwd(), ...payload }));
      } catch (err) {
        warn(`${eventName} failed: ${err?.message ?? err}`);
        resolve();
      }
    });
  chain = chain.then(run, run);
  return chain;
}

// Hooks fire for sub-agents too, with their own session id. Only the session
// this extension joined is a session to trace.
const ours = (input) => !input?.sessionId || input.sessionId === session?.sessionId;

function startSession(timestamp) {
  if (started) return;
  started = true;
  send("sessionStart", { timestamp }, { surface: true });
}

// Resolves once the session's last sends have run, so a hook can hold the
// extension open for them.
function endSession(timestamp) {
  if (ended) return chain;
  ended = true;
  endTurn(timestamp);
  return send("sessionEnd", { timestamp });
}

// Ids of events delivered live while catchUp reads the history, so an event
// seen both ways is buffered once. Null once catchUp is done.
let liveIds = new Set();
// When a turn ended live while catchUp was still reading the history, and no
// live prompt had opened one. That turn is the history's, so catchUp ends it.
let closedDuringCatchUp = null;
// Ids catchUp took from the history, which the live stream may still deliver.
let replayed = new Set();

// Persisted events that only follow a finished main-agent turn. session.idle
// would be the natural marker, but it is ephemeral and never in the history.
const turnClosed = (e) =>
  !e.agentId &&
  (e.type === "session.idle" ||
    e.type === "session.shutdown" ||
    e.type === "session.usage_checkpoint" ||
    (e.type === "hook.start" && e.data?.hookType === "sessionEnd"));

// A prompt older than this is not the one that started the extension.
const CATCH_UP_WINDOW_MS = 60_000;

// The app starts the extension when the first prompt is sent, so that prompt
// is already history by the time the extension listens. Recover the turn in
// progress from the history rather than lose every session's first turn.
async function catchUp() {
  try {
    const history = await session.getEvents();
    // A live prompt opened a turn of its own, or the session is already over.
    if (turnOpen || ended) return;
    let start = -1;
    let closed = false;
    for (let i = history.length - 1; i >= 0; i--) {
      const e = history[i];
      if (turnClosed(e)) break;
      if (isPrompt(e)) {
        start = i;
        break;
      }
    }
    if (start < 0) {
      // The first turn can end before the extension listens: a request that
      // fails at once (an unsupported model, a quota) closes it in
      // milliseconds. A session whose only prompt is that one, and that was not
      // resumed, has reported nothing yet, so its closed turn is still ours.
      const prompts = history.filter(isPrompt);
      if (prompts.length !== 1 || history.some((e) => e.type === "session.resume")) return;
      start = history.indexOf(prompts[0]);
      closed = true;
    }
    const prompt = history[start];
    if (Date.now() - Date.parse(prompt.timestamp) > CATCH_UP_WINDOW_MS) return;
    const later = history.slice(start + 1).filter((e) => !liveIds.has(e.id));
    const earlier = later.filter((e) => KEEP[e.type]);
    for (const e of later) if (e.id) replayed.add(e.id);
    buffer = [...earlier.map(slim), ...buffer].slice(0, MAX_EVENTS);
    // Steering that arrived live while the history was read follows the
    // history's own.
    const live = steered;
    openTurn(prompt);
    steered = [...later.filter(isSteering).map((e) => e.data?.content ?? ""), ...live];
    if (closed) endTurn(history[history.length - 1].timestamp);
  } catch (err) {
    warn(`could not read the session history: ${err?.message ?? err}`);
  } finally {
    liveIds = null;
    if (closedDuringCatchUp) endTurn(closedDuringCatchUp.timestamp, closedDuringCatchUp.aborted);
    closedDuringCatchUp = null;
  }
}

// A message the user sent into the running turn, rather than one that starts
// its own (delivery "idle", or "queued" until the turn before it ends).
const isSteering = (e) => e.type === "user.message" && !e.agentId && e.data?.delivery === "steering";
// A main-agent message that starts a turn.
const isPrompt = (e) => e.type === "user.message" && !e.agentId && !isSteering(e);

function openTurn(message) {
  turnOpen = true;
  turnPrompt = message.data?.content ?? "";
  steered = [];
  send("userPromptSubmitted", { timestamp: message.timestamp, prompt: clip(turnPrompt) });
}

// Ends the turn, unless catchUp may still recover the one it belongs to.
// aborted is session.idle's flag for a run the user cancelled.
function closeTurn(timestamp, aborted = false) {
  if (liveIds && !turnOpen) closedDuringCatchUp = { timestamp, aborted };
  else endTurn(timestamp, aborted);
}

function endTurn(timestamp, aborted = false) {
  const events = buffer;
  const open = turnOpen;
  const payload = { timestamp, events };
  if (aborted) payload.aborted = true;
  if (steered.length) payload.prompt = clip([turnPrompt, ...steered].join("\n"));
  buffer = [];
  replayed = new Set();
  turnOpen = false;
  steered = [];
  if (open) send("turnEnd", payload);
}

try {
  session = await joinSession({
    skillDirectories: [join(HERE, "skills")],
    hooks: {
      onSessionStart: (input) => {
        try {
          if (ours(input)) startSession(new Date(input.timestamp ?? Date.now()).toISOString());
        } catch (err) {
          warn(`onSessionStart failed: ${err?.message ?? err}`);
        }
      },
      // The app fires sessionEnd with reason "complete" after every prompt's
      // run, so only a user exit ends the session; the rest end the turn.
      onSessionEnd: (input) => {
        try {
          if (!ours(input)) return;
          const timestamp = new Date(input.timestamp ?? Date.now()).toISOString();
          if (input.reason !== "user_exit") return closeTurn(timestamp);
          // The app stops the extension once the session ends (SIGTERM, then
          // SIGKILL 5s later). Returning the sends keeps it alive until
          // turnEnd and sessionEnd have run, within that window.
          return Promise.race([
            endSession(timestamp),
            new Promise((resolve) => setTimeout(resolve, EXIT_BUDGET_MS).unref()),
          ]);
        } catch (err) {
          warn(`onSessionEnd failed: ${err?.message ?? err}`);
        }
      },
    },
  });

  // The extension can load after the session began (a reload, or an install
  // mid-session), when onSessionStart has already fired. The binary treats a
  // repeated sessionStart as a no-op.
  startSession(new Date().toISOString());

  session.on((event) => {
    try {
      if (liveIds && event.id) liveIds.add(event.id);
      if (replayed.size && replayed.has(event.id)) return;
      if (KEEP[event.type]) {
        if (buffer.length < MAX_EVENTS) buffer.push(slim(event));
        return;
      }
      switch (event.type) {
        case "user.message":
          // Sub-agent prompts carry an agentId; they are part of the turn
          // already open, not a new one.
          if (event.agentId) return;
          if (isSteering(event) && (turnOpen || liveIds)) {
            steered.push(event.data?.content ?? "");
            return;
          }
          closedDuringCatchUp = null;
          endTurn(event.timestamp);
          openTurn(event);
          return;
        case "session.idle":
          closeTurn(event.timestamp, event.data?.aborted === true);
          return;
        case "session.shutdown":
          endSession(event.timestamp);
          return;
      }
    } catch (err) {
      warn(`event ${event?.type} dropped: ${err?.message ?? err}`);
    }
  });

  await catchUp();
} catch (err) {
  warn(`extension failed to start: ${err?.message ?? err}`);
}
