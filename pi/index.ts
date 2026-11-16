// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import { spawn } from "node:child_process";
import { chmodSync, existsSync } from "node:fs";
import { resolve } from "node:path";
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

/** Truncate to a byte budget without leaving a split UTF-8 sequence behind. */
function clip(value: string, max: number): string {
  if (Buffer.byteLength(value, "utf8") <= max) return value;
  // Every UTF-16 unit encodes to at least one byte, so the first `max` units
  // cover the first `max` bytes without encoding a multi-megabyte value.
  return Buffer.from(value.slice(0, max), "utf8")
    .subarray(0, max)
    .toString("utf8")
    .replace(/\uFFFD+$/, "");
}

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

const isTextBlock = (b: unknown): b is { type: "text"; text: string } =>
  typeof b === "object" &&
  b !== null &&
  (b as { type?: unknown }).type === "text" &&
  typeof (b as { text?: unknown }).text === "string";

/**
 * Render a tool payload or message content as text, never throwing. An array
 * keeps only its text blocks, so image data, thinking and tool-call blocks
 * never enter telemetry, even with I/O capture on.
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

const count = (n: unknown): number =>
  typeof n === "number" && Number.isSafeInteger(n) && n >= 0 ? n : 0;
const optionalCount = (n: unknown): number | undefined =>
  typeof n === "number" && Number.isSafeInteger(n) && n >= 0 ? n : undefined;

/** Pi's usage, as Pi and pi-subagents both report it, in the helper's names. */
function toUsage(u: unknown): Usage {
  const v = (typeof u === "object" && u !== null ? u : {}) as Record<
    string,
    unknown
  >;
  return {
    input: count(v.input),
    output: count(v.output),
    cache_read: count(v.cacheRead),
    cache_write: count(v.cacheWrite),
    cache_write_1h: optionalCount(v.cacheWrite1h),
    reasoning: optionalCount(v.reasoning),
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
    // A child that exported itself has its own session_start before the
    // parent's tool call ends, because Pi starts the child's session first.
    // Each file is looked up once, so the set does not grow with children.
    const exported =
      typeof r.sessionFile === "string" &&
      exportedSessions.delete(r.sessionFile);
    const usage = (exported ? {} : r.usage) as Record<string, unknown>;
    const model = !exported && validString(r.model) ? r.model : undefined;
    // The first slash separates the provider, as in splitModel in
    // internal/source/pi.
    const provider = model?.split("/")[0];
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
 * Collect each user turn and hand it to `send` once Pi settles.
 *
 * A turn starts at before_agent_start and ends at agent_settled, not at
 * agent_end: Pi can retry, compact and retry, or run queued follow-ups after
 * agent_end, and all of that is still the same user turn.
 */
export function register(pi: ExtensionAPI, send: (turn: Turn) => void) {
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

  // A run that no prompt started, for example after an extension sent a
  // message while Pi was busy starting, still gets a turn.
  pi.on("agent_start", (_event, ctx) => {
    if (!state) begin(ctx);
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
    const text = asText(message.content).trim();
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

  pi.on("session_start", (_event, ctx) => {
    const file = ctx.sessionManager.getSessionFile();
    if (typeof file === "string") exportedSessions.add(file);
  });

  pi.on("tool_execution_end", (event, ctx) => {
    const active = state;
    const call = active?.running.get(event.toolCallId);
    if (!active || !call || call.name !== event.toolName) return;
    active.running.delete(event.toolCallId);
    active.turn.tools.push({
      ...call,
      end: new Date().toISOString(),
      error: event.isError === true,
      output: take(active, () => asText(event.result?.content)),
      subagents:
        event.toolName === "subagent"
          ? subagentsOf(ctx, event.result?.details)
          : undefined,
    });
  });

  pi.on("agent_settled", (event) => {
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
    // grows control-heavy text, so shed content, never the turn.
    if (Buffer.byteLength(JSON.stringify(turn), "utf8") > maxEnvelope) {
      turn.truncated = true;
      turn.prompt = undefined;
      turn.response = undefined;
      turn.tools = turn.tools.map(({ input, output, ...tool }) => tool);
    }
    send(turn);
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
  const binary = existsSync(perPlatform)
    ? perPlatform
    : beside(`./pi-on-event${exe}`);
  // `pi install` from git or npm does not keep the exec bit everywhere.
  try {
    chmodSync(binary, 0o755);
  } catch {
    // Read-only or absent: spawn reports the real problem.
  }

  let cwd: string | undefined;
  let warned = false;
  let warn: (() => void) | undefined;
  let live = 0;
  const pending = new Set<Promise<void>>();

  pi.on("session_start", (_event, ctx) => {
    cwd = ctx.cwd;
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
  // survives that, but the envelope must reach its stdin first.
  pi.on("session_shutdown", async () => {
    if (pending.size === 0) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    await Promise.race([
      Promise.allSettled(pending),
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, shutdownGrace);
      }),
    ]);
    clearTimeout(timer);
  });

  register(pi, (turn) => {
    if (live >= maxHelpers) return;
    let child;
    try {
      child = spawn(binary, [], {
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
      warn?.();
      return;
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
    child.on("error", () => end(true));
    child.on("close", (code) => end(code !== 0));
    const written = new Promise<void>((resolve) => {
      child.stdin.on("error", () => resolve());
      child.stdin.end(JSON.stringify(turn), () => resolve());
    });
    pending.add(written);
    void written.then(() => pending.delete(written));
  });
}
