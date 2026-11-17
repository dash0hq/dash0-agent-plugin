// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import { spawn } from "node:child_process";
import { chmodSync, existsSync } from "node:fs";
import type { Socket } from "node:net";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type {
  ExtensionAPI,
  ExtensionContext,
} from "@earendil-works/pi-coding-agent";

type Usage = {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  cache_write_1h?: number;
  reasoning?: number;
};
type Call = {
  provider: string;
  model: string;
  response_model?: string;
  start: string;
  end: string;
  stop_reason: string;
  thinking_level?: string;
  /** "subscription" when the provider auth is a subscription login. */
  billing_mode?: "subscription" | "unknown";
  usage: Usage;
};
type Tool = {
  id: string;
  parent_id?: string;
  name: string;
  start: string;
  end: string;
  error: boolean;
  /** Index in Turn.calls of the model call that requested this tool. */
  call?: number;
  input?: string;
  output?: string;
  /** The skill whose SKILL.md this call read. */
  skill?: string;
  /** The children that a foreground pi-subagents call ran. */
  subagents?: Subagent[];
  /** Model usage that the tool reported for its own work. */
  usage?: Usage;
};
/** One foreground pi-subagents child, from the subagent tool result. */
type Subagent = {
  agent: string;
  /** "<provider>/<model>", as pi-subagents reports it. */
  model?: string;
  usage: Usage;
  turns: number;
  billing_mode?: "subscription" | "unknown";
  failed: boolean;
};
/** The envelope that cmd/pi-on-event reads. internal/source/pi defines it. */
export type Turn = {
  session_id: string;
  parent_session_id?: string;
  start: string;
  end: string;
  status: "done" | "error" | "aborted";
  calls: Call[];
  tools: Tool[];
  truncated: boolean;
  prompt?: string;
  response?: string;
  /** The skill a /skill:name command loaded into the starting prompt. */
  skill?: string;
};
type Active = {
  turn: Omit<Turn, "end" | "status">;
  /** Tool call ID to the index of the model call that requested it. */
  requestedBy: Map<string, number>;
  running: Map<string, Omit<Tool, "end" | "error" | "output">>;
  response: string;
  /** Content bytes already attached to this turn, before JSON escaping. */
  content: number;
};

/** Model calls and tool calls kept per turn, as the helper accepts. */
const maxItems = 512;

// The helper rejects envelopes over 1 MiB, which would lose the whole turn,
// and JSON escaping can double a control-character-heavy string. A 256 KiB
// content budget keeps the worst case inside that limit. The per-field cap
// matches the exporter's MaxContentBytes, so nothing is dropped here that the
// exporter would keep.
const maxField = 16 * 1024;
const maxContent = 256 * 1024;
const maxEnvelope = 768 * 1024;

/** Children kept per subagent call, as the helper accepts. */
const maxSubagents = 64;

/**
 * Session files that an instance of this extension exports. A foreground
 * child that loads this extension, for example through pi-subagents'
 * `defaultSubagentOnlyExtensions`, runs in the parent's process but with its
 * own copy of this module, so only a global is shared with the parent.
 */
const exportedSessions = ((globalThis as Record<symbol, unknown>)[
  Symbol.for("dash0.pi.exported-sessions")
] ??= new Set<string>()) as Set<string>;

/** Detached helpers alive at once. Stops a runaway, not ordinary overlap. */
const maxHelpers = 16;

/** How long session_shutdown waits for envelopes still being written. */
const shutdownGrace = 2000;

// BEGIN content helpers, kept identical in amp/index.ts and pi/index.ts.
// Amp's installers fetch index.ts as one file, so the two cannot import a
// shared module. test/consistency/bridge_test.go fails when they differ.
// The per-turn budget (take) and the envelope shedding stay outside this
// block: they read each bridge's own turn state and envelope shape, which
// differ. That test checks their limits against the Go helpers instead.

/** Truncate to a byte budget without leaving a split UTF-8 sequence behind. */
function clip(value: string, max: number): string {
  // byteLength measures without copying, so the common case never allocates
  // a Buffer for the whole field.
  if (Buffer.byteLength(value, "utf8") <= max) return value;
  // Slice first: every UTF-16 unit encodes to at least one byte, so the first
  // `max` units always cover the first `max` bytes. Encoding the whole value
  // would copy a multi-megabyte tool output to keep 16 KiB of it.
  return Buffer.from(value.slice(0, max), "utf8")
    .subarray(0, max)
    .toString("utf8")
    .replace(/\uFFFD+$/, "");
}

const isTextBlock = (b: unknown): b is { type: "text"; text: string } =>
  typeof b === "object" &&
  b !== null &&
  (b as { type?: unknown }).type === "text" &&
  typeof (b as { text?: unknown }).text === "string";

/**
 * Render a tool payload or message content as text, never throwing on odd
 * values. Any array keeps only its text blocks, even when it mixes an image
 * block with an unknown element, so inline image data and image URLs never
 * enter telemetry, even with I/O capture on.
 */
function asText(value: unknown): string {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  if (Array.isArray(value))
    return value
      .filter(isTextBlock)
      .map((block) => block.text)
      .join("\n");
  try {
    return JSON.stringify(value) ?? "";
  } catch {
    return "";
  }
}

/**
 * Text of an assistant message's text blocks, joined. Thinking and tool blocks
 * are skipped by the same filter as a tool payload, so an image or thinking
 * block is dropped by one rule only. Both runtimes type this content as an
 * array; any other value is malformed and gives no text, never its JSON.
 */
function messageText(message: { content?: unknown } | undefined): string {
  return Array.isArray(message?.content) ? asText(message.content).trim() : "";
}
// END content helpers

/**
 * Claim budget for one content field. Returns undefined once the turn's
 * budget is spent, so a long tail of tool output costs the turn its content
 * and not its delivery. The value is a thunk so that a spent budget skips the
 * rendering too.
 */
function take(state: Active, render: () => string): string | undefined {
  if (state.content >= maxContent) {
    state.turn.truncated = true;
    return undefined;
  }
  const value = render();
  if (!value) return undefined;
  const text = clip(value, maxField);
  if (text !== value) state.turn.truncated = true;
  const size = Buffer.byteLength(text, "utf8");
  if (state.content + size > maxContent) {
    state.turn.truncated = true;
    return undefined;
  }
  state.content += size;
  return text;
}

const count = (n: unknown): number =>
  typeof n === "number" && Number.isSafeInteger(n) && n >= 0 ? n : 0;
const optionalCount = (n: unknown): number | undefined =>
  typeof n === "number" && Number.isSafeInteger(n) && n >= 0 ? n : undefined;

/**
 * Pi's usage, as Pi and pi-subagents both report it, in the helper's names.
 * A part count that is larger than its total is dropped alone, as the helper
 * also does, so the other counts of the call stay.
 */
function toUsage(u: unknown): Usage {
  const v = (typeof u === "object" && u !== null ? u : {}) as Record<
    string,
    unknown
  >;
  const output = count(v.output);
  const cacheWrite = count(v.cacheWrite);
  const cacheWrite1h = optionalCount(v.cacheWrite1h);
  const reasoning = optionalCount(v.reasoning);
  return {
    input: count(v.input),
    output,
    cache_read: count(v.cacheRead),
    cache_write: cacheWrite,
    cache_write_1h:
      cacheWrite1h !== undefined && cacheWrite1h <= cacheWrite
        ? cacheWrite1h
        : undefined,
    reasoning:
      reasoning !== undefined && reasoning <= output ? reasoning : undefined,
  };
}
const validString = (s: unknown): s is string =>
  typeof s === "string" && s.length > 0 && s.length <= 256;
const iso = (ms: number) => new Date(ms).toISOString();

/** The session ID in a Pi session file name, `<timestamp>_<id>.jsonl`. */
export function sessionIDFromPath(path: unknown): string | undefined {
  if (typeof path !== "string") return undefined;
  const name = path.split(/[\\/]/).pop() ?? "";
  return /^\d{4}-\d{2}-\d{2}T[\d-]+Z_(.+)\.jsonl$/.exec(name)?.[1];
}

/**
 * How a provider bills, as Pi knows it. Pi marks an OAuth login that a
 * subscription backs, for example Claude Pro/Max through the claude-code
 * provider or ChatGPT through openai-codex. An API key does not prove list
 * price, because a proxy can sit behind it, so it stays "unknown".
 */
export function billingMode(
  ctx: ExtensionContext,
  provider: string,
): "subscription" | "unknown" {
  try {
    const registry = ctx.modelRegistry;
    const oauth = registry.isUsingOAuth({ provider } as Parameters<
      typeof registry.isUsingOAuth
    >[0]);
    return oauth && registry.getProvider(provider)?.auth.oauth?.isSubscription
      ? "subscription"
      : "unknown";
  } catch {
    return "unknown";
  }
}

/**
 * The foreground children in a pi-subagents `subagent` tool result. This is
 * the result shape of the pi-subagents extension, not a Pi API, so anything
 * unexpected gives no children. A background launch has no result rows: its
 * child is a separate session that this extension exports by itself.
 *
 * A child that exported its own session keeps its agent but loses its model
 * and tokens here, so they are not counted twice.
 */
export function subagentsOf(
  ctx: ExtensionContext,
  details: unknown,
): Subagent[] | undefined {
  try {
    return readSubagents(ctx, details);
  } catch {
    return undefined;
  }
}

function readSubagents(
  ctx: ExtensionContext,
  details: unknown,
): Subagent[] | undefined {
  // Another extension can also name its tool "subagent". Only the result
  // shape of pi-subagents counts: a run ID, a mode, and rows that each carry
  // the child's agent, usage object and exit code.
  const d = details as
    | { results?: unknown; runId?: unknown; mode?: unknown }
    | undefined;
  const rows = d?.results;
  if (!Array.isArray(rows) || !validString(d?.runId) || !validString(d?.mode))
    return undefined;
  const out: Subagent[] = [];
  for (const row of rows.slice(0, maxSubagents)) {
    if (typeof row !== "object" || row === null) continue;
    const r = row as Record<string, unknown>;
    if (
      !validString(r.agent) ||
      typeof r.usage !== "object" ||
      r.usage === null ||
      typeof r.exitCode !== "number"
    )
      continue;
    // A child that exported itself handed its turn to a helper before the
    // parent's tool call ends, because the child settles first. Each file is
    // looked up once, so the set does not grow with children.
    const exported =
      typeof r.sessionFile === "string" &&
      exportedSessions.delete(r.sessionFile);
    const usage = (exported ? {} : r.usage) as Record<string, unknown>;
    const model = !exported && validString(r.model) ? r.model : undefined;
    // The first slash separates the provider, as in splitModel in
    // internal/source/pi. A model without a slash names no provider.
    const slash = model?.indexOf("/") ?? -1;
    const provider = slash > 0 ? model?.slice(0, slash) : undefined;
    out.push({
      agent: r.agent,
      model,
      usage: toUsage(usage),
      turns: count(usage.turns),
      billing_mode: provider ? billingMode(ctx, provider) : undefined,
      failed: r.exitCode !== 0 || typeof r.error === "string",
    });
  }
  return out.length ? out : undefined;
}

/**
 * The tools of @tintinweb/pi-subagents that report the billed tokens of its
 * children as their own usage, when its `reportUsage` setting is on.
 */
const childrenTotalTools: ReadonlySet<string> = new Set([
  "Agent",
  "get_subagent_result",
  "steer_subagent",
]);

/**
 * The model usage that a tool reported for its own work, from Pi's tool
 * result. Pi reports the usage of each nested call on that call's own result,
 * so each token is on one tool. Undefined when the tool reported no tokens.
 *
 * pi-subagents reports the total of its children as the usage of its
 * `subagent` result and of its management results, such as the wait tool's.
 * Those children are already counted: a foreground child by its row on the
 * subagent call, a background child by its own session. So these results give
 * no usage.
 *
 * @tintinweb/pi-subagents, with its `reportUsage` setting, reports the billed
 * tokens of its children as the usage of the results of the tools in
 * `childrenTotalTools`. Its children run in the parent's process, load the
 * parent's extensions, and so export their own sessions. So these results
 * also give no usage.
 */
export function toolUsage(
  toolName: string,
  result: unknown,
): Usage | undefined {
  try {
    if (toolName === "subagent" || childrenTotalTools.has(toolName))
      return undefined;
    const r = result as { usage?: unknown; details?: unknown } | undefined;
    const d = r?.details as { mode?: unknown; results?: unknown } | undefined;
    if (d?.mode === "management" && Array.isArray(d.results)) return undefined;
    const usage = toUsage(r?.usage);
    return usage.input + usage.output + usage.cache_read + usage.cache_write > 0
      ? usage
      : undefined;
  } catch {
    return undefined;
  }
}

/** The skill that a /skill:name command expanded into, as Pi writes it. */
const skillBlock = /^<skill name="([^"]{1,128})" location="/;

/** Absolute paths, so a relative read argument is resolved against cwd. */
function skillAt(
  skills: ReadonlyMap<string, string>,
  cwd: string | undefined,
  path: unknown,
): string | undefined {
  if (typeof path !== "string" || skills.size === 0) return undefined;
  const p = path.startsWith("@") ? path.slice(1) : path;
  return skills.get(cwd ? resolve(cwd, p) : p);
}

/**
 * Make the turn fit the envelope limit. Content goes first, then tools
 * without children or usage, then child rows, then the other tools. A child
 * row and a tool's usage carry tokens, so they are kept the longest. Returns
 * the encoded turn.
 */
export function fit(turn: Turn): string {
  let json = "";
  const fits = () => {
    json = JSON.stringify(turn);
    return Buffer.byteLength(json, "utf8") <= maxEnvelope;
  };
  if (fits()) return json;
  turn.truncated = true;
  turn.prompt = undefined;
  turn.response = undefined;
  turn.tools = turn.tools.map(({ input, output, ...tool }) => tool);
  // Each step halves what it keeps until the turn fits, so a turn of any
  // size is encoded a few dozen times at most.
  const all = turn.tools;
  const carriesTokens = (t: Tool) => t.subagents || t.usage;
  const plain = all.filter((t) => !carriesTokens(t));
  for (let n = plain.length; !fits() && n > 0; ) {
    n >>= 1;
    const kept = new Set(plain.slice(0, n));
    turn.tools = all.filter((t) => carriesTokens(t) || kept.has(t));
  }
  const parents = turn.tools;
  for (let rows = maxSubagents; !fits() && rows > 0; ) {
    rows >>= 1;
    turn.tools = parents.map((t) =>
      t.subagents && t.subagents.length > rows
        ? { ...t, subagents: rows ? t.subagents.slice(0, rows) : undefined }
        : t,
    );
  }
  const rest = turn.tools;
  for (let n = rest.length; !fits() && n > 0; ) {
    n >>= 1;
    turn.tools = rest.slice(0, n);
  }
  return json;
}

/**
 * Collect each user turn and hand it to `send` once Pi settles. `send`
 * returns false when it could not hand the turn to a helper.
 *
 * A turn starts at before_agent_start and ends at agent_settled, not at
 * agent_end: Pi can retry, compact and retry, or run queued follow-ups after
 * agent_end, and all of that is still the same user turn.
 */
export function register(
  pi: ExtensionAPI,
  send: (turn: Turn, json: string) => boolean,
) {
  let state: Active | undefined;
  // SKILL.md path to skill name, from the skills Pi loaded for this run. A
  // read of any other file called SKILL.md is not a skill load.
  let skills = new Map<string, string>();
  let cwd: string | undefined;

  const begin = (ctx: ExtensionContext): Active | undefined => {
    const session = ctx.sessionManager.getSessionId();
    if (!validString(session)) return undefined;
    state = {
      turn: {
        session_id: session,
        parent_session_id: sessionIDFromPath(
          ctx.sessionManager.getHeader()?.parentSession,
        ),
        start: new Date().toISOString(),
        calls: [],
        tools: [],
        truncated: false,
      },
      requestedBy: new Map(),
      running: new Map(),
      response: "",
      content: 0,
    };
    return state;
  };

  pi.on("before_agent_start", (event, ctx) => {
    const active = begin(ctx);
    cwd = event.systemPromptOptions?.cwd ?? ctx.cwd;
    skills = new Map(
      (event.systemPromptOptions?.skills ?? [])
        .filter((s) => validString(s?.name) && typeof s?.filePath === "string")
        .map((s) => [s.filePath, s.name]),
    );
    if (active && event.messageType === "user") {
      const prompt = event.prompt;
      active.turn.prompt = take(active, () => prompt);
      active.turn.skill =
        typeof prompt === "string" ? skillBlock.exec(prompt)?.[1] : undefined;
    }
  });

  pi.on("message_end", (event, ctx) => {
    const active = state;
    const message = event.message;
    if (!active || message?.role !== "assistant") return;
    if (!validString(message.provider) || !validString(message.model)) return;
    if (active.turn.calls.length >= maxItems) {
      active.turn.truncated = true;
      return;
    }
    const index = active.turn.calls.length;
    const started = count(message.timestamp) || Date.now();
    const duration = optionalCount(message.durationMs);
    const usage = message.usage;
    active.turn.calls.push({
      provider: message.provider,
      model: message.model,
      response_model: validString(message.responseModel)
        ? message.responseModel
        : undefined,
      start: iso(started),
      end: iso(duration === undefined ? Date.now() : started + duration),
      stop_reason: String(message.stopReason ?? ""),
      thinking_level: validString(message.providerThinkingLevel)
        ? message.providerThinkingLevel
        : undefined,
      billing_mode: billingMode(ctx, message.provider),
      usage: toUsage(usage),
    });
    if (Array.isArray(message.content)) {
      for (const block of message.content) {
        if (block?.type === "toolCall" && validString(block.id))
          active.requestedBy.set(block.id, index);
      }
    }
    // Only the last answer is kept, and it is charged to the budget at the
    // end, so a long run of intermediate answers costs nothing here.
    const text = messageText(message);
    if (text) active.response = text;
  });

  pi.on("tool_execution_start", (event) => {
    const active = state;
    if (
      !active ||
      !validString(event.toolCallId) ||
      !validString(event.toolName) ||
      active.running.has(event.toolCallId)
    )
      return;
    if (active.running.size + active.turn.tools.length >= maxItems) {
      active.turn.truncated = true;
      return;
    }
    const parent = validString(event.parentToolCallId)
      ? event.parentToolCallId
      : undefined;
    // A nested call, such as one a codemode script made, belongs to the model
    // call that requested its parent. The parent starts first, so it is known.
    const requester =
      active.requestedBy.get(event.toolCallId) ??
      (parent === undefined ? undefined : active.requestedBy.get(parent));
    if (requester !== undefined)
      active.requestedBy.set(event.toolCallId, requester);
    active.running.set(event.toolCallId, {
      id: event.toolCallId,
      parent_id: parent,
      name: event.toolName,
      start: new Date().toISOString(),
      call: requester,
      skill:
        event.toolName === "read"
          ? skillAt(skills, cwd, event.args?.path)
          : undefined,
      input: take(active, () => asText(event.args)),
    });
  });

  pi.on("tool_execution_end", (event, ctx) => {
    const active = state;
    const call = active?.running.get(event.toolCallId);
    if (!active || !call || call.name !== event.toolName) return;
    active.running.delete(event.toolCallId);
    const subagents =
      event.toolName === "subagent"
        ? subagentsOf(ctx, event.result?.details)
        : undefined;
    // Rows over maxSubagents are dropped, as calls and tools over their caps
    // are, and the turn says so in the same way.
    const results = (event.result?.details as { results?: unknown } | undefined)
      ?.results;
    if (subagents && Array.isArray(results) && results.length > maxSubagents)
      active.turn.truncated = true;
    active.turn.tools.push({
      ...call,
      end: new Date().toISOString(),
      error: event.isError === true,
      output: take(active, () => asText(event.result?.content)),
      subagents,
      usage: toolUsage(event.toolName, event.result),
    });
  });

  pi.on("agent_settled", (event, ctx) => {
    const active = state;
    state = undefined;
    if (!active) return;
    const last = active.turn.calls.at(-1);
    const turn: Turn = {
      ...active.turn,
      tools: [...active.turn.tools],
      end: new Date().toISOString(),
      status: event.aborted
        ? "aborted"
        : last?.stop_reason === "error"
          ? "error"
          : "done",
    };
    // Pi ends every call it starts, aborted ones included, before it settles.
    // A call still open here is reported as failed, not dropped.
    const end = new Date().toISOString();
    for (const call of active.running.values())
      turn.tools.push({ ...call, end, error: true });
    const answer = active.response;
    turn.response = take(active, () => answer);
    turn.truncated ||= active.turn.truncated;
    // IDs, names and counts are outside the content budget, and escaping
    // grows control-heavy text, so shed parts of the turn, never the turn.
    const json = fit(turn);
    // A parent in this process leaves out the tokens of a child that handed
    // its turn to a helper. A child that could not keep them in the parent.
    if (send(turn, json)) {
      try {
        const file = ctx.sessionManager.getSessionFile();
        if (typeof file === "string") exportedSessions.add(file);
      } catch {
        // No session file: no parent can look this session up.
      }
    }
  });

  // A session switch, fork or reload replaces this extension instance, so a
  // turn still open here can never settle.
  pi.on("session_shutdown", () => {
    state = undefined;
  });
}

export default function (pi: ExtensionAPI) {
  const exe = process.platform === "win32" ? ".exe" : "";
  const beside = (name: string) =>
    fileURLToPath(new URL(name, import.meta.url));
  // Same names as the release assets, so one directory can serve several
  // platforms; the plain name is the single-platform install.
  const os = process.platform === "win32" ? "windows" : process.platform;
  // An architecture without a release asset finds no per-platform helper and
  // uses the plain name.
  const arch = process.arch === "x64" ? "amd64" : process.arch;
  const perPlatform = beside(`./pi-on-event-${os}-${arch}${exe}`);
  const plain = beside(`./pi-on-event${exe}`);
  const local = existsSync(perPlatform)
    ? perPlatform
    : existsSync(plain)
      ? plain
      : undefined;
  // `pi install` from git or npm does not keep the exec bit everywhere.
  try {
    if (local) chmodSync(local, 0o755);
  } catch {
    // Read-only: spawn reports the real problem.
  }
  // The npm package ships no helper. Its bootstrap downloads the release
  // helper on the first turn, verifies its checksum and caches it. On POSIX
  // the bootstrap then execs the helper with the envelope on stdin. On
  // Windows it only prints the helper's path, and the helper is spawned
  // directly: through a detached PowerShell, the envelope would be decoded
  // with the console code page and the helper would open a console window.
  const windows = process.platform === "win32";
  // A full path, because Windows looks for a bare name in the cwd (the
  // user's project) before PATH.
  const powershell = join(
    process.env.SystemRoot ?? "C:\\Windows",
    "System32",
    "WindowsPowerShell",
    "v1.0",
    "powershell.exe",
  );
  // The resolved helper. A turn is handed off only once a helper has
  // started with its envelope, so it is read synchronously at settle time.
  let helper: string | undefined;
  let resolving: Promise<string | undefined> | undefined;
  const resolveHelper = () =>
    (resolving ??= new Promise<string | undefined>((done) => {
      let out = "";
      try {
        const child = spawn(
          powershell,
          [
            "-NoProfile",
            "-NonInteractive",
            "-ExecutionPolicy",
            "Bypass",
            "-File",
            beside("./pi-on-event.ps1"),
            "--resolve-binary",
          ],
          {
            stdio: ["ignore", "pipe", "ignore"],
            detached: true,
            windowsHide: true,
          },
        );
        // A download must not keep a Pi that is done from exiting, and must
        // outlive it: Windows kills a child that is not detached when Pi
        // exits. The download then completes for the next run.
        child.unref();
        // A pipe is a Socket, which Node types as a plain Readable.
        (child.stdout as Socket).unref();
        child.stdout.setEncoding("utf8");
        child.stdout.on("data", (data: string) => (out += data));
        child.on("error", () => done(undefined));
        child.on("close", (code) =>
          done(code === 0 && out.trim() !== "" ? out.trim() : undefined),
        );
      } catch {
        done(undefined);
      }
    }).then((path) => {
      // The bootstrap fails open with no path. The next turn tries again.
      if (path === undefined) resolving = undefined;
      helper = path;
      return path;
    }));

  let cwd: string | undefined;
  let warned = false;
  let warn: (() => void) | undefined;
  let live = 0;
  const pending = new Set<Promise<void>>();

  pi.on("session_start", (_event, ctx) => {
    cwd = ctx.cwd;
    // Started with the session, so the helper is usually ready before the
    // first turn settles.
    if (windows && !local) void resolveHelper();
    warn = () => {
      if (warned) return;
      warned = true;
      // An export can fail after Pi has ended this session, and Pi then
      // throws on any use of its context. The throw would happen in a
      // child-process callback, which ends the Pi process.
      try {
        if (ctx.hasUI) ctx.ui.notify("dash0: telemetry unavailable", "warning");
      } catch {
        // The session is gone, and so is the place to show the notice.
      }
    };
  });

  // Print mode exits as soon as the prompt is answered. A detached helper
  // survives that, but the envelope must reach its stdin first. Only a quit
  // ends the process: after a session switch, fork or reload, the write
  // continues, so the switch does not wait.
  pi.on("session_shutdown", async (event) => {
    if (event.reason !== "quit" || pending.size === 0) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    await Promise.race([
      Promise.allSettled(pending),
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, shutdownGrace);
      }),
    ]);
    clearTimeout(timer);
  });

  const track = (promise: Promise<void>) => {
    pending.add(promise);
    void promise.then(() => pending.delete(promise));
  };

  /** Starts a detached helper; returns when its envelope is written. */
  const start = (
    command: string,
    args: string[],
    json: string,
  ): Promise<void> | undefined => {
    let child;
    try {
      child = spawn(command, args, {
        // The helper resolves the project configuration and the repository
        // from its working directory.
        cwd,
        stdio: ["pipe", "ignore", "ignore"],
        // Detached so an export outlives a Pi process that exits right after
        // the turn, and so no export ever holds up the next prompt.
        detached: true,
        windowsHide: true,
      });
    } catch {
      return undefined;
    }
    live++;
    child.unref();
    let ended = false;
    const end = (failed: boolean) => {
      if (ended) return;
      ended = true;
      live--;
      if (failed) warn?.();
    };
    child.on("error", () => {
      // The cached helper is gone, so the next turn resolves it again.
      resolving = undefined;
      helper = undefined;
      end(true);
    });
    child.on("close", (code) => end(code !== 0));
    return new Promise<void>((resolve) => {
      child.stdin.on("error", () => resolve());
      child.stdin.end(json, () => resolve());
    });
  };

  register(pi, (_turn, json) => {
    // The turn is lost, so the user hears about it as for a failed export.
    if (live >= maxHelpers) {
      warn?.();
      return false;
    }
    const [command, args] = local
      ? [local, []]
      : !windows
        ? ["bash", [beside("./pi-on-event.sh")]]
        : [helper, []];
    // On Windows before the helper is resolved, the turn is lost rather than
    // queued: a parent drops the tokens of a child that reports a handoff,
    // so a handoff is reported only for a helper that has started.
    const written =
      command === undefined ? undefined : start(command, args, json);
    if (!written) {
      if (command === undefined) void resolveHelper();
      warn?.();
      return false;
    }
    track(written);
    return true;
  });
}
