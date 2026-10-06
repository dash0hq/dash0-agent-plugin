// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";
import type {
  PluginAPI,
  PluginEventMap,
  PluginExecutorKind,
  ThreadMessageID,
} from "@ampcode/plugin";

export const description =
  "Dash0 traces for completed Amp turns and tools, with optional per-model export usage.";

type Tool = {
  id: string;
  name: string;
  start: string;
  end: string;
  status: PluginEventMap["tool.result"]["status"];
  input?: string;
  output?: string;
};
export type Turn = {
  thread_id: string;
  id: ThreadMessageID;
  start: string;
  end: string;
  status: PluginEventMap["agent.end"]["status"];
  executor: PluginExecutorKind;
  tools: Tool[];
  assistant_ids: ThreadMessageID[];
  truncated: boolean;
  prompt?: string;
  response?: string;
};
type Active = {
  turn: Omit<Turn, "end" | "status">;
  calls: Map<
    string,
    Pick<Tool, "id" | "name" | "start" | "input"> & { finished?: boolean }
  >;
  /** Content bytes already attached to this turn, before JSON escaping. */
  content: number;
};
const limit = 512;

// The helper rejects envelopes over 1 MiB outright, which would lose the whole
// turn, and JSON escaping can roughly double a control-character-heavy string.
// A 256 KiB content budget keeps the worst case inside that limit. The
// per-field cap matches the exporter's own MaxContentBytes, so nothing is
// dropped here that would have survived the other side.
const maxField = 16 * 1024;
const maxContent = 256 * 1024;
const maxEnvelope = 768 * 1024;

// How long agent.end waits for the helper before handing off and letting it
// finish on its own. See the spawn site for why this exists.
//
// It is also what keeps the `sending` cap below meaningful: that cap counts
// deliveries still being waited on, so a zero grace would resolve every send
// immediately and leave nothing to count.
const handoffGrace = 2000;

/** Turns delivered at once: waited-on in register(), live helpers at the spawn site. */
const maxDeliveries = 4;

// One diagnostic, reported from two places: the turn's own failure path and,
// after the handoff, the helper's. transport.test.ts asserts it verbatim.
const unavailable = "dash0: telemetry unavailable";

/** Truncate to a byte budget without leaving a split UTF-8 sequence behind. */
function clip(value: string, max: number): string {
  // byteLength measures without copying, and take() re-measures the result, so
  // the common case never allocates a Buffer for the whole field.
  if (Buffer.byteLength(value, "utf8") <= max) return value;
  return Buffer.from(value, "utf8")
    .subarray(0, max)
    .toString("utf8")
    .replace(/�+$/, "");
}

/** Text of a message's text blocks, joined; thinking and tool blocks are skipped. */
function messageText(message: { content?: unknown }): string {
  // Same block filter as a tool payload, so an image or thinking block is
  // dropped by exactly one rule rather than by two that can drift apart.
  return Array.isArray(message?.content) ? asText(message.content).trim() : "";
}
const validID = (id: unknown): id is ThreadMessageID =>
  typeof id === "string"
    ? id.length > 0 && id.length <= 256
    : typeof id === "number" && Number.isSafeInteger(id) && id >= 0;
const validString = (s: unknown): s is string =>
  typeof s === "string" && s.length > 0 && s.length <= 256;

/**
 * Claim budget for one content field. Returns undefined once the turn's budget
 * is spent, so a long tail of tool output costs the turn its content rather
 * than its delivery.
 */
function take(state: Active, value: string): string | undefined {
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

const isBlock = (b: unknown): b is { type: string } =>
  typeof b === "object" &&
  b !== null &&
  typeof (b as { type?: unknown }).type === "string";
const isTextBlock = (b: unknown): b is { type: "text"; text: string } =>
  isBlock(b) &&
  b.type === "text" &&
  typeof (b as { text?: unknown }).text === "string";

/**
 * Render an arbitrary tool payload as text, never throwing on odd values. Any
 * array keeps only its text blocks — mixing an image block with an unknown
 * element must not fall through to JSON.stringify — so inline image payloads
 * and image URLs never enter telemetry even when I/O capture is on.
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

export function register(amp: PluginAPI, send: (turn: Turn) => Promise<void>) {
  const active = new Map<string, Active>();
  let sending = 0;
  amp.on("agent.start", (event) => {
    const thread = event?.thread?.id;
    if (!validString(thread) || !validID(event?.id)) return {};
    const previous = active.get(thread);
    if (previous?.turn.id === event.id) return {};
    if (!previous && active.size >= 32) return {};
    // Retire unfinished work; toolUseID is unique within a thread.
    const state: Active = {
      turn: {
        thread_id: thread,
        id: event.id,
        start: new Date().toISOString(),
        executor: amp.system.executor.kind,
        tools: [],
        assistant_ids: [],
        truncated: false,
      },
      calls: new Map(),
      content: 0,
    };
    if (typeof event.message === "string") {
      state.turn.prompt = take(state, event.message);
    }
    active.set(thread, state);
    return {};
  });
  amp.on("tool.call", (event) => {
    const state = active.get(event?.thread?.id);
    if (
      state &&
      validString(event.toolUseID) &&
      validString(event.tool) &&
      !state.calls.has(event.toolUseID)
    ) {
      if (state.calls.size >= limit) state.turn.truncated = true;
      else
        state.calls.set(event.toolUseID, {
          id: event.toolUseID,
          name: event.tool,
          start: new Date().toISOString(),
          input: take(state, asText(event.input)),
        });
    }
    return { action: "allow" };
  });
  amp.on("tool.result", (event) => {
    const state = active.get(event?.thread?.id);
    const call = state?.calls.get(event?.toolUseID);
    if (
      state &&
      call &&
      !call.finished &&
      call.name === event.tool &&
      ["done", "error", "cancelled"].includes(event.status)
    ) {
      call.finished = true;
      state.turn.tools.push({
        id: call.id,
        name: call.name,
        start: call.start,
        end: new Date().toISOString(),
        status: event.status,
        input: call.input,
        output: take(state, asText(event.output) || asText(event.error)),
      });
    }
  });
  amp.on("agent.end", async (event) => {
    const state = active.get(event?.thread?.id);
    if (!state || state.turn.id !== event.id) return;
    active.delete(event.thread.id);
    if (!["done", "error", "cancelled"].includes(event.status)) return;
    const turn: Turn = {
      ...state.turn,
      end: new Date().toISOString(),
      status: event.status,
    };
    let answer = "";
    if (Array.isArray(event.messages)) {
      for (const message of event.messages) {
        if (message?.role !== "assistant" || !validID(message.id)) continue;
        if (turn.assistant_ids.length >= limit) {
          turn.truncated = true;
          break;
        }
        turn.assistant_ids.push(message.id);
        // The turn's answer is the last assistant text, matching what the other
        // harnesses put on their chat span. Thinking blocks are never read, and
        // only the answer is charged to the budget.
        const text = messageText(message);
        if (text) answer = text;
      }
    }
    turn.response = take(state, answer);
    // take() runs after the spread above, and the assistant_ids loop sets the
    // flag on the copy, so merge both directions rather than assigning.
    turn.truncated ||= state.turn.truncated;
    // Tool ids, names, and assistant ids are outside the content budget, and
    // JSON escaping expands control-heavy text, so the encoded envelope can
    // still pass 1 MiB and be rejected whole. Shed content, never the turn.
    if (Buffer.byteLength(JSON.stringify(turn), "utf8") > maxEnvelope) {
      turn.truncated = true;
      turn.prompt = undefined;
      turn.response = undefined;
      turn.tools = turn.tools.map(({ input, output, ...tool }) => tool);
    }
    if (sending >= maxDeliveries) {
      amp.logger.log("dash0: turn dropped, exporter busy");
      return;
    }
    sending++;
    try {
      await send(turn);
    } catch {
      amp.logger.log(unavailable);
    } finally {
      sending--;
    }
  });
}

export default function (amp: PluginAPI) {
  const binary = fileURLToPath(
    new URL(
      process.platform === "win32" ? "./amp-on-event.exe" : "./amp-on-event",
      import.meta.url,
    ),
  );
  const cwd = amp.system.workspaceRoot
    ? amp.helpers.filePathFromURI(amp.system.workspaceRoot)
    : undefined;
  const debug = /^(true|1)$/i.test(
    process.env.AMP_PLUGIN_OPTION_DEBUG?.trim() ?? "",
  );
  // The helper outlives the handoff, so register()'s cap — which counts turns
  // still being waited on — no longer bounds live helper processes. Count them
  // here, where they actually end, or a burst of threads can leave dozens of
  // detached exports running at once.
  let live = 0;
  register(
    amp,
    (turn) =>
      new Promise<void>((resolve, reject) => {
        if (live >= maxDeliveries) {
          reject(new Error("helper capacity"));
          return;
        }
        live++;
        // Resolve configuration and VCS metadata in the event's workspace.
        const started = Date.now();
        const child = spawn(binary, [], {
          cwd,
          stdio: ["pipe", "ignore", "ignore"],
          // Detached and unref'd so the helper outlives this process. With
          // usage export on it polls `amp threads export` for up to twenty
          // seconds, and in `amp -x` mode the CLI exits as soon as the turn
          // ends — without this the helper would be torn down mid-poll and the
          // turn's spans lost.
          detached: true,
        });
        child.unref();
        let handedOff = false;
        let ended = false;
        // Stop *waiting* after the grace period, but let the helper run on.
        // agent.end is awaited by Amp, so anything waited for here is latency
        // the user feels between turns. A healthy helper finishes well inside
        // this (measured 110-200 ms without usage export), so the common path
        // is unchanged; only the slow usage poll is released early.
        const handoff = setTimeout(() => {
          handedOff = true;
          resolve();
        }, handoffGrace);
        handoff.unref();
        // "error" and "close" can both fire for one child, so capacity is
        // released once and a failure is reported once. Once the turn has been
        // handed off there is no promise left to reject, so a late failure has
        // to report itself or it is silent.
        const end = (error?: Error) => {
          if (ended) return;
          ended = true;
          live--;
          clearTimeout(handoff);
          if (!error) resolve();
          else if (handedOff) amp.logger.log(unavailable);
          else reject(error);
        };
        child.on("error", end);
        child.stdin.on("error", end);
        child.on("close", (code, signal) => {
          if (debug)
            amp.logger.log(
              `dash0: helper ${JSON.stringify({ code, signal, elapsed_ms: Date.now() - started })}`,
            );
          end(code === 0 ? undefined : new Error("helper failed"));
        });
        child.stdin.end(JSON.stringify(turn));
      }),
  );
}
