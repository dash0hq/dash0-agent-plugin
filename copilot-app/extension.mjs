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
// How long a send waits for the bootstrap's output after the bootstrap exits.
const EXIT_GRACE_MS = 1_000;
// How long the sessionEnd hook waits for the final sends, under the 5s the app
// allows between SIGTERM and SIGKILL.
const EXIT_BUDGET_MS = 4_000;

// The session events the Go adapter reads, and the data keys it reads from
// them. Dropping the rest keeps the stdin payload small and keeps fields a
// later SDK adds from reaching the binary unannounced.
const KEEP = {
  "assistant.usage": ["model", "isAuto", "inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "reasoningTokens", "parentToolCallId"],
  "assistant.message": ["content", "model", "parentToolCallId"],
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
        let done = false;
        const finish = () => {
          if (done) return;
          done = true;
          child.stderr.destroy();
          for (const line of stderr.split(/\r?\n/)) {
            if (!line.trim()) continue;
            if (surface && line.startsWith("dash0:")) session?.log(withHint(line)).catch(() => {});
            else warn(line);
          }
          resolve();
        };
        child.stderr.on("data", (d) => (stderr += d));
        child.on("error", (err) => {
          warn(`could not run ${BOOTSTRAP}: ${err.message}`);
          finish();
        });
        child.on("close", finish);
        // close waits for every holder of stderr, and a download the bootstrap
        // started can outlive it. Once the bootstrap exits, give its output a
        // moment, then stop holding up the chain.
        child.on("exit", () => setTimeout(finish, EXIT_GRACE_MS).unref());
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

// The app has no other way to point the user at the setup skill.
const withHint = (line) =>
  /^dash0: (telemetry is not active|no team configured)/.test(line) ? `${line} Run /dash0-configure.` : line;

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

// Live events that arrive while catchUp reads the history, in order. Null once
// catchUp is done, which then handles them as if they had just arrived.
let pending = [];
// How many of them had arrived when the usage metrics were read: the metrics
// count those, and not the ones after.
let beforeMetrics = 0;
// Ids catchUp took from the history, which the live stream may still deliver.
const replayed = new Set();
// Ends catchUp's wait for the history early, when the user exits.
let stopCatchUp = () => {};
let caughtUp = Promise.resolve();

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
// Live events wait for the history, so a read that hangs must not hold them.
const HISTORY_TIMEOUT_MS = 30_000;

// The app starts the extension when the first prompt is sent, so that prompt
// is already history by the time the extension listens. Recover the turn in
// progress from the history rather than lose every session's first turn, then
// handle the live events that arrived meanwhile, in order.
async function catchUp() {
  try {
    const read = Promise.all([
      session.getEvents(),
      usageMetrics().then((m) => {
        beforeMetrics = pending.length;
        return m;
      }),
    ]);
    const stop = new Promise((_, reject) => {
      stopCatchUp = () => reject(new Error("the session ended"));
      setTimeout(stopCatchUp, HISTORY_TIMEOUT_MS).unref();
    });
    const [history, metrics] = await Promise.race([read, stop]);
    recover(history, metrics);
  } catch (err) {
    warn(`could not read the session history: ${err?.message ?? err}`);
  } finally {
    const live = pending;
    pending = null;
    for (const e of live) handle(e);
  }
}

// Opens the turn the history shows in progress, with the events and usage it
// had before the extension listened.
function recover(history, metrics) {
  // The history runs up to the read, so it overlaps the live events. Only what
  // precedes the first of them was missed; the rest arrives live.
  const live = new Set(pending.map((e) => e.id).filter(Boolean));
  const cut = history.findIndex((e) => live.has(e.id));
  if (cut >= 0) history = history.slice(0, cut);
  for (const e of history) if (e.id) replayed.add(e.id);

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
  const first = history.filter(isPrompt).length === 1 && !history.some((e) => e.type === "session.resume");
  if (start < 0) {
    // The first turn can end before the extension listens: a request that
    // fails at once (an unsupported model, a quota) closes it in
    // milliseconds. A session whose only prompt is that one, and that was not
    // resumed, has reported nothing yet, so its closed turn is still ours.
    if (!first) return;
    start = history.findIndex(isPrompt);
    closed = true;
  }
  const prompt = history[start];
  if (Date.now() - Date.parse(prompt.timestamp) > CATCH_UP_WINDOW_MS) return;
  const later = history.slice(start + 1);
  const missed = first ? usageMissed(metrics, prompt.timestamp, pending.slice(0, beforeMetrics)) : [];
  openTurn(prompt);
  buffer = [...missed, ...later.filter((e) => KEEP[e.type]).map(slim)].slice(0, MAX_EVENTS);
  steered = later.filter(isSteering).map((e) => e.data?.content ?? "");
  if (closed) endTurn(history[history.length - 1].timestamp);
}

const TOKEN_KEYS = ["inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "reasoningTokens"];

// The session's usage metrics, or null. The SDK marks them experimental.
async function usageMetrics() {
  try {
    return await session.rpc?.usage?.getMetrics?.();
  } catch (err) {
    warn(`could not read the session usage: ${err?.message ?? err}`);
    return null;
  }
}

// assistant.usage is ephemeral, so the history has none of the usage spent
// before the extension listened. The session's metrics have it all: for the
// session's first turn, they less what arrived live before them is what was
// missed. Returns
// assistant.usage events per model carrying that difference.
function usageMissed(metrics, timestamp, seen) {
  const perModel = metrics?.agentMetrics?.main?.modelMetrics;
  if (!perModel) return [];
  const out = [];
  for (const [model, m] of Object.entries(perModel)) {
    const data = { model };
    for (const k of TOKEN_KEYS) {
      let n = Number(m?.usage?.[k]) || 0;
      for (const e of seen) {
        if (e.type === "assistant.usage" && !e.agentId && !e.data?.parentToolCallId && e.data?.model === model) n -= Number(e.data[k]) || 0;
      }
      if (n > 0) data[k] = n;
    }
    if (Object.keys(data).length > 1) out.push({ type: "assistant.usage", timestamp, data });
  }
  return out;
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

// aborted is session.idle's flag for a run the user cancelled.
function endTurn(timestamp, aborted = false) {
  const events = buffer;
  const open = turnOpen;
  const payload = { timestamp, events };
  if (aborted) payload.aborted = true;
  if (steered.length) payload.prompt = clip([turnPrompt, ...steered].join("\n"));
  buffer = [];
  turnOpen = false;
  steered = [];
  if (open) send("turnEnd", payload);
}

function handle(event) {
  try {
    if (ended || replayed.has(event.id)) return;
    if (KEEP[event.type]) {
      if (buffer.length < MAX_EVENTS) buffer.push(slim(event));
      return;
    }
    switch (event.type) {
      case "user.message":
        // Sub-agent prompts carry an agentId; they are part of the turn
        // already open, not a new one.
        if (event.agentId) return;
        if (isSteering(event) && turnOpen) {
          steered.push(event.data?.content ?? "");
          return;
        }
        endTurn(event.timestamp);
        openTurn(event);
        return;
      case "session.idle":
        endTurn(event.timestamp, event.data?.aborted === true);
        return;
      case "session.shutdown":
        endSession(event.timestamp);
        return;
    }
  } catch (err) {
    warn(`event ${event?.type} dropped: ${err?.message ?? err}`);
  }
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
      // run, so only a user exit ends the session. The turn is left to
      // session.idle: the run's session.error can arrive after this hook, and
      // only idle carries the aborted flag.
      onSessionEnd: (input) => {
        try {
          if (!ours(input)) return;
          const timestamp = new Date(input.timestamp ?? Date.now()).toISOString();
          if (input.reason !== "user_exit") return;
          // The app stops the extension once the session ends (SIGTERM, then
          // SIGKILL 5s later). Returning the sends keeps it alive until
          // turnEnd and sessionEnd have run, within that window.
          // A history read still pending would hold the final sends back.
          stopCatchUp();
          return Promise.race([
            caughtUp.then(() => endSession(timestamp)),
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
    if (pending) pending.push(event);
    else handle(event);
  });

  caughtUp = catchUp();
  await caughtUp;
} catch (err) {
  warn(`extension failed to start: ${err?.message ?? err}`);
}
