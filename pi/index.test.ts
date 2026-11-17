// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import { expect, test } from "bun:test";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import {
  billingMode,
  fit,
  register,
  sessionIDFromPath,
  subagentsOf,
  toolUsage,
  type Turn,
} from "./index";

type Handler = (event: unknown, ctx: unknown) => unknown;

function setup(
  session = "019a-session",
  parentSession?: string,
  handedOff = true,
) {
  const handlers = new Map<string, Handler>();
  const sent: Turn[] = [];
  const encoded: string[] = [];
  const api = {
    on: (name: string, handler: Handler) => handlers.set(name, handler),
  } as unknown as ExtensionAPI;
  register(api, (turn, json) => {
    sent.push(turn);
    encoded.push(json);
    return handedOff;
  });
  const ctx = {
    sessionManager: {
      getSessionId: () => session,
      getHeader: () => ({ parentSession }),
      getSessionFile: () => `/sessions/${session}.jsonl`,
    },
    modelRegistry: registry({ "claude-code": [true, true] }),
  };
  const fire = (name: string, event: Record<string, unknown> = {}) =>
    handlers.get(name)?.({ type: name, ...event }, ctx);
  return { sent, encoded, fire, handlers };
}

/** A model registry where each provider maps to [OAuth in use, subscription]. */
function registry(auth: Record<string, [boolean, boolean]>) {
  return {
    isUsingOAuth: (model: { provider: string }) =>
      auth[model.provider]?.[0] ?? false,
    getProvider: (provider: string) =>
      provider in auth
        ? { auth: { oauth: { isSubscription: auth[provider][1] } } }
        : undefined,
  };
}

test("only a subscription login is reported as a subscription", () => {
  const ctx = (r: unknown) =>
    ({ modelRegistry: r }) as unknown as Parameters<typeof billingMode>[0];
  const r = registry({
    "claude-code": [true, true],
    "openai-codex": [true, true],
    anthropic: [false, true],
    "corp-sso": [true, false],
  });
  expect(billingMode(ctx(r), "claude-code")).toBe("subscription");
  expect(billingMode(ctx(r), "openai-codex")).toBe("subscription");
  // An API key on a provider that also offers a subscription login.
  expect(billingMode(ctx(r), "anthropic")).toBe("unknown");
  expect(billingMode(ctx(r), "corp-sso")).toBe("unknown");
  expect(billingMode(ctx(r), "missing")).toBe("unknown");
  expect(billingMode(ctx(undefined), "claude-code")).toBe("unknown");
});

const assistant = (
  model: string,
  provider: string,
  content: unknown[],
  extra: Record<string, unknown> = {},
) => ({
  message: {
    role: "assistant",
    provider,
    model,
    content,
    stopReason: "stop",
    timestamp: Date.parse("2026-11-13T12:00:01Z"),
    durationMs: 1500,
    usage: {
      input: 10,
      output: 5,
      cacheRead: 100,
      cacheWrite: 7,
      cacheWrite1h: 7,
      reasoning: 2,
    },
    ...extra,
  },
});

test("a turn runs from the prompt until Pi settles, across agent_end", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "the ask" });
  fire("agent_start");
  fire(
    "message_end",
    assistant("gpt-5.5", "openai", [
      { type: "thinking", thinking: "SECRET-THOUGHT" },
      { type: "toolCall", id: "call_1", name: "bash", arguments: {} },
    ]),
  );
  fire("tool_execution_start", {
    toolCallId: "call_1",
    toolName: "bash",
    args: { command: "ls" },
  });
  fire("tool_execution_end", {
    toolCallId: "call_1",
    toolName: "bash",
    isError: false,
    result: {
      content: [
        { type: "text", text: "listing" },
        { type: "image", data: "SECRET-BASE64", mimeType: "image/png" },
      ],
    },
  });
  // A retry or queued follow-up after agent_end is still this turn.
  fire("agent_end", { messages: [] });
  expect(sent).toHaveLength(0);
  fire("agent_start");
  fire(
    "message_end",
    assistant("claude-sonnet-5", "anthropic", [
      { type: "text", text: "the answer" },
    ]),
  );
  fire("agent_end", { messages: [] });
  fire("agent_settled", { aborted: false });

  expect(sent).toHaveLength(1);
  const turn = sent[0];
  expect(turn.session_id).toBe("019a-session");
  expect(turn.status).toBe("done");
  expect(turn.prompt).toBe("the ask");
  expect(turn.response).toBe("the answer");
  expect(turn.calls.map((c) => [c.provider, c.model])).toEqual([
    ["openai", "gpt-5.5"],
    ["anthropic", "claude-sonnet-5"],
  ]);
  expect(turn.calls.map((c) => c.billing_mode)).toEqual(["unknown", "unknown"]);
  expect(turn.calls[0]).toMatchObject({
    start: "2026-11-13T12:00:01.000Z",
    end: "2026-11-13T12:00:02.500Z",
    usage: {
      input: 10,
      output: 5,
      cache_read: 100,
      cache_write: 7,
      cache_write_1h: 7,
      reasoning: 2,
    },
  });
  expect(turn.tools).toHaveLength(1);
  expect(turn.tools[0]).toMatchObject({
    id: "call_1",
    name: "bash",
    error: false,
    call: 0,
    input: '{"command":"ls"}',
    output: "listing",
  });
  const encoded = JSON.stringify(turn);
  expect(encoded).not.toContain("SECRET");
});

test("an aborted turn is sent with its unfinished calls", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "stop me" });
  fire(
    "message_end",
    assistant("claude-sonnet-5", "anthropic", [], { stopReason: "aborted" }),
  );
  fire("tool_execution_start", { toolCallId: "t", toolName: "bash" });
  fire("agent_settled", { aborted: true });
  expect(sent).toHaveLength(1);
  expect(sent[0].status).toBe("aborted");
  // Pi ends every call before it settles; one still open is a failure.
  expect(sent[0].tools).toMatchObject([{ id: "t", error: true }]);
  expect(sent[0].calls[0].stop_reason).toBe("aborted");
});

test("a failed last model call marks the turn as an error", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "x" });
  fire("message_end", assistant("m", "p", [], { stopReason: "error" }));
  fire("agent_settled", { aborted: false });
  expect(sent[0].status).toBe("error");
});

test("user, tool-result and custom messages are not model calls", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "x" });
  fire("message_end", { message: { role: "user", content: "x" } });
  fire("message_end", { message: { role: "toolResult", content: [] } });
  fire("message_end", { message: { role: "custom", content: "x" } });
  fire("agent_settled", { aborted: false });
  expect(sent[0].calls).toHaveLength(0);
});

test("a nested tool call belongs to the model call of its parent", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "x" });
  fire(
    "message_end",
    assistant("m", "p", [
      { type: "toolCall", id: "code", name: "codemode", arguments: {} },
    ]),
  );
  fire("tool_execution_start", { toolCallId: "code", toolName: "codemode" });
  fire("tool_execution_start", {
    toolCallId: "code/1",
    toolName: "read",
    parentToolCallId: "code",
  });
  fire("tool_execution_end", {
    toolCallId: "code/1",
    toolName: "read",
    isError: true,
    parentToolCallId: "code",
  });
  fire("tool_execution_end", { toolCallId: "code", toolName: "codemode" });
  fire("agent_settled", { aborted: false });
  expect(
    sent[0].tools.map((t) => [t.id, t.parent_id, t.call, t.error]),
  ).toEqual([
    ["code/1", "code", 0, true],
    ["code", undefined, 0, false],
  ]);
});

// Pi 1.5.0 emits agent_start only inside a run that before_agent_start
// opened, so events without that are not a turn.
test("a run without before_agent_start makes no turn", () => {
  const { sent, fire } = setup();
  fire("agent_start");
  fire("message_end", assistant("m", "p", [{ type: "text", text: "hi" }]));
  fire("agent_settled", { aborted: false });
  expect(sent).toHaveLength(0);
});

test("a custom-message run carries no prompt", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "custom", message: {} });
  fire("agent_settled", { aborted: false });
  expect(sent[0].prompt).toBeUndefined();
});

test("session shutdown drops an open turn", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "x" });
  fire("session_shutdown", { reason: "fork" });
  fire("agent_settled", { aborted: false });
  expect(sent).toHaveLength(0);
});

test("a forked session names its parent", () => {
  const { sent, fire } = setup(
    "child-id",
    "/home/u/.pi/agent/sessions/--x--/2026-11-13T10-35-09-135Z_parent-id.jsonl",
  );
  fire("before_agent_start", { messageType: "user", prompt: "x" });
  fire("agent_settled", { aborted: false });
  expect(sent[0].parent_session_id).toBe("parent-id");
});

test("session IDs come only from Pi's session file names", () => {
  expect(sessionIDFromPath("/a/2026-11-13T10-35-09-135Z_abc.jsonl")).toBe(
    "abc",
  );
  expect(sessionIDFromPath("C:\\a\\2026-11-13T10-35-09-135Z_abc.jsonl")).toBe(
    "abc",
  );
  expect(sessionIDFromPath("/a/notes.jsonl")).toBeUndefined();
  expect(sessionIDFromPath(undefined)).toBeUndefined();
});

test("content stays inside the envelope budget and is marked truncated", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "p" });
  for (let i = 0; i < 40; i++) {
    fire("tool_execution_start", {
      toolCallId: `t${i}`,
      toolName: "read",
      args: { path: "x".repeat(20_000) },
    });
    fire("tool_execution_end", {
      toolCallId: `t${i}`,
      toolName: "read",
      result: { content: [{ type: "text", text: "é".repeat(20_000) }] },
    });
  }
  fire("agent_settled", { aborted: false });
  const turn = sent[0];
  expect(turn.truncated).toBe(true);
  expect(turn.tools).toHaveLength(40);
  expect(Buffer.byteLength(JSON.stringify(turn))).toBeLessThan(1 << 20);
  for (const tool of turn.tools) {
    expect(Buffer.byteLength(tool.output ?? "")).toBeLessThanOrEqual(16 * 1024);
    expect(tool.output ?? "").not.toContain("\uFFFD");
  }
});

test("a skill is named by the route that loaded it", () => {
  const { sent, fire } = setup();
  const systemPromptOptions = {
    cwd: "/work/repo",
    skills: [
      { name: "qa-echo", filePath: "/work/repo/.pi/skills/qa-echo/SKILL.md" },
      { name: "pdf-tools", filePath: "/home/u/.pi/agent/skills/pdf/SKILL.md" },
    ],
  };
  fire("before_agent_start", {
    messageType: "user",
    prompt:
      '<skill name="qa-echo" location="/work/repo/.pi/skills/qa-echo/SKILL.md">\nReferences are relative to /work/repo.\n\nbody\n</skill>\n\ngo',
    systemPromptOptions,
  });
  const read = (id: string, path: string) => {
    fire("tool_execution_start", {
      toolCallId: id,
      toolName: "read",
      args: { path },
    });
    fire("tool_execution_end", { toolCallId: id, toolName: "read" });
  };
  read("r1", "/home/u/.pi/agent/skills/pdf/SKILL.md");
  read("r2", ".pi/skills/qa-echo/SKILL.md");
  // Another file called SKILL.md is not a loaded skill.
  read("r3", "/tmp/other/SKILL.md");
  fire("tool_execution_start", {
    toolCallId: "b1",
    toolName: "bash",
    args: { command: "cat /home/u/.pi/agent/skills/pdf/SKILL.md" },
  });
  fire("tool_execution_end", { toolCallId: "b1", toolName: "bash" });
  fire("agent_settled", { aborted: false });
  expect(sent[0].skill).toBe("qa-echo");
  expect(sent[0].tools.map((t) => [t.id, t.skill])).toEqual([
    ["r1", "pdf-tools"],
    ["r2", "qa-echo"],
    ["r3", undefined],
    ["b1", undefined],
  ]);
});

test("a prompt that only mentions a skill block is not a command", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", {
    messageType: "user",
    prompt: 'explain <skill name="x" location="y">',
  });
  fire("agent_settled", { aborted: false });
  expect(sent[0].skill).toBeUndefined();
});

test("malformed events are ignored, never thrown", () => {
  const { sent, fire } = setup("");
  expect(() => {
    fire("before_agent_start", { messageType: "user", prompt: "x" });
    fire("message_end", {});
    fire("tool_execution_start", {});
    fire("tool_execution_end", {});
    fire("agent_settled", { aborted: false });
  }).not.toThrow();
  expect(sent).toHaveLength(0);
});

/** A foreground pi-subagents result, as 0.88.0 shapes it. */
const subagentResult = (rows: unknown[]) => ({
  content: [{ type: "text", text: "done" }],
  details: { mode: "single", runId: "run-1", results: rows },
});
const childRow = (extra: Record<string, unknown> = {}) => ({
  agent: "delegate",
  exitCode: 0,
  model: "claude-code/claude-haiku-4-5",
  sessionFile: "/sessions/parent/run-0/session.jsonl",
  usage: {
    input: 4,
    output: 460,
    cacheRead: 5145,
    cacheWrite: 5211,
    cost: 0.013,
    turns: 2,
  },
  ...extra,
});

test("a foreground subagent call carries its children's totals", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "delegate" });
  fire("tool_execution_start", { toolCallId: "c1", toolName: "subagent" });
  fire("tool_execution_end", {
    toolCallId: "c1",
    toolName: "subagent",
    isError: false,
    result: subagentResult([
      childRow(),
      childRow({ agent: "scout", exitCode: 1, model: "openai-codex/gpt-5.5" }),
    ]),
  });
  fire("agent_settled", { aborted: false });
  expect(sent[0].tools[0].subagents).toEqual([
    {
      agent: "delegate",
      model: "claude-code/claude-haiku-4-5",
      usage: { input: 4, output: 460, cache_read: 5145, cache_write: 5211 },
      turns: 2,
      billing_mode: "subscription",
      failed: false,
    },
    {
      agent: "scout",
      model: "openai-codex/gpt-5.5",
      usage: { input: 4, output: 460, cache_read: 5145, cache_write: 5211 },
      turns: 2,
      billing_mode: "unknown",
      failed: true,
    },
  ]);
});

test("children over the row cap mark the turn as truncated", () => {
  const send = (rows: number) => {
    const { sent, fire } = setup();
    fire("before_agent_start", { messageType: "user", prompt: "delegate" });
    fire("tool_execution_start", { toolCallId: "c1", toolName: "subagent" });
    fire("tool_execution_end", {
      toolCallId: "c1",
      toolName: "subagent",
      isError: false,
      result: subagentResult(Array.from({ length: rows }, () => childRow())),
    });
    fire("agent_settled", { aborted: false });
    return sent[0];
  };
  const full = send(64);
  expect(full.tools[0].subagents).toHaveLength(64);
  expect(full.truncated).toBe(false);
  const over = send(65);
  expect(over.tools[0].subagents).toHaveLength(64);
  expect(over.truncated).toBe(true);
});

test("a background launch and other tools have no children", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "x" });
  fire("tool_execution_start", { toolCallId: "c1", toolName: "subagent" });
  fire("tool_execution_end", {
    toolCallId: "c1",
    toolName: "subagent",
    result: {
      details: {
        mode: "single",
        runId: "run-1",
        results: [],
        asyncId: "run-1",
      },
    },
  });
  fire("tool_execution_start", { toolCallId: "c2", toolName: "bash" });
  fire("tool_execution_end", {
    toolCallId: "c2",
    toolName: "bash",
    result: subagentResult([childRow()]),
  });
  fire("agent_settled", { aborted: false });
  expect(sent[0].tools.map((t) => t.subagents)).toEqual([undefined, undefined]);
});

test("malformed subagent results give no children, never throw", () => {
  const ctx = { modelRegistry: undefined } as unknown as Parameters<
    typeof subagentsOf
  >[0];
  const row = { agent: "a", exitCode: 0, usage: { input: 1 } };
  for (const details of [
    undefined,
    null,
    "x",
    { mode: "single", runId: "r", results: "x" },
    {
      mode: "single",
      runId: "r",
      results: [null, 1, { agent: "" }, { agent: 5 }],
    },
    // A row without a usage object or an exit code is not a finished child.
    { mode: "single", runId: "r", results: [{ agent: "a", exitCode: 0 }] },
    { mode: "single", runId: "r", results: [{ agent: "a", usage: {} }] },
  ])
    expect(subagentsOf(ctx, details)).toBeUndefined();
  const throwing = {
    get results() {
      throw new Error("getter");
    },
  };
  expect(subagentsOf(ctx, throwing)).toBeUndefined();
  // Negative counts are not counts.
  expect(
    subagentsOf(ctx, {
      mode: "single",
      runId: "r",
      results: [{ ...row, usage: { input: -1 } }],
    })?.[0].usage,
  ).toEqual({ input: 0, output: 0, cache_read: 0, cache_write: 0 });
});

// Another extension can also call its tool "subagent". Only the result shape
// of pi-subagents gives children, so another tool keeps the old behaviour:
// one execute_tool span, no invoke_agent span.
test("another extension's subagent tool gives no children", () => {
  const ctx = { modelRegistry: undefined } as unknown as Parameters<
    typeof subagentsOf
  >[0];
  const row = { agent: "a", exitCode: 0, usage: { input: 1 } };
  expect(subagentsOf(ctx, { results: [row] })).toBeUndefined();
  expect(subagentsOf(ctx, { mode: "single", results: [row] })).toBeUndefined();
  expect(subagentsOf(ctx, { runId: "r", results: [row] })).toBeUndefined();
  expect(
    subagentsOf(ctx, { mode: "single", runId: "r", results: [row] }),
  ).toHaveLength(1);
});

/** A child session runs one turn, and its parent then reads the child's row. */
function childThenParent(childHandedOff: boolean) {
  const child = setup("019a-child", undefined, childHandedOff);
  child.fire("session_start", { reason: "startup" });
  child.fire("before_agent_start", { messageType: "user", prompt: "task" });
  child.fire("agent_settled", { aborted: false });
  const parent = setup();
  parent.fire("before_agent_start", { messageType: "user", prompt: "x" });
  parent.fire("tool_execution_start", {
    toolCallId: "c1",
    toolName: "subagent",
  });
  parent.fire("tool_execution_end", {
    toolCallId: "c1",
    toolName: "subagent",
    result: subagentResult([
      childRow({ sessionFile: "/sessions/019a-child.jsonl" }),
      childRow(),
    ]),
  });
  parent.fire("agent_settled", { aborted: false });
  return parent.sent[0].tools[0].subagents ?? [];
}

// With pi-subagents' defaultSubagentOnlyExtensions, this extension also loads
// into a foreground child, in the same process. The child then exports its
// own session, and the parent must not count the same tokens again.
test("a child that exported itself keeps its agent but not its tokens", () => {
  const [own, other] = childThenParent(true);
  expect(own.agent).toBe("delegate");
  expect(own.model).toBeUndefined();
  expect(own.usage).toEqual({
    input: 0,
    output: 0,
    cache_read: 0,
    cache_write: 0,
  });
  expect(other.model).toBe("claude-code/claude-haiku-4-5");
});

// A child that started but could not hand its turn to a helper, for example
// at the helper cap, exported nothing. Its tokens then stay with the parent.
test("a child whose export did not start keeps its tokens in the parent", () => {
  const [own] = childThenParent(false);
  expect(own.model).toBe("claude-code/claude-haiku-4-5");
  expect(own.usage.output).toBe(460);
});

// The helper drops a part count that is larger than its total. The bridge
// drops it first, so the call keeps its other tokens.
test("a part count larger than its total is dropped alone", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "x" });
  fire(
    "message_end",
    assistant("m", "p", [], {
      usage: {
        input: 1,
        output: 5,
        cacheRead: 0,
        cacheWrite: 3,
        cacheWrite1h: 4,
        reasoning: 6,
      },
    }),
  );
  fire(
    "message_end",
    assistant("m", "p", [], {
      usage: {
        input: 1,
        output: 5,
        cacheRead: 0,
        cacheWrite: 3,
        cacheWrite1h: 3,
        reasoning: 5,
      },
    }),
  );
  fire("agent_settled", { aborted: false });
  expect(sent[0].calls.map((c) => c.usage)).toEqual([
    { input: 1, output: 5, cache_read: 0, cache_write: 3 },
    {
      input: 1,
      output: 5,
      cache_read: 0,
      cache_write: 3,
      cache_write_1h: 3,
      reasoning: 5,
    },
  ]);
});

// splitModel in internal/source/pi gives no provider for a model without a
// slash, so the bridge looks up no billing mode for it.
test("a child model without a provider gets no billing mode", () => {
  const ctx = {
    modelRegistry: registry({ "gpt-5": [true, true] }),
  } as unknown as Parameters<typeof subagentsOf>[0];
  const rows = subagentsOf(ctx, {
    mode: "single",
    runId: "r",
    results: [
      { agent: "a", exitCode: 0, usage: {}, model: "gpt-5" },
      { agent: "b", exitCode: 0, usage: {}, model: "/gpt-5" },
    ],
  });
  expect(rows?.map((r) => r.billing_mode)).toEqual([undefined, undefined]);
});

const helperLimit = 1 << 20;

// Shedding content is not enough when the turn has many tools with long IDs
// and many children. The bridge then drops tools and child rows until the
// envelope is under the helper's limit.
test("an oversized turn is cut until it fits the helper's limit", () => {
  const { sent, encoded, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "p" });
  const long = (i: number) => `${i}-${"x".repeat(250)}`;
  for (let i = 0; i < 400; i++) {
    fire("tool_execution_start", { toolCallId: long(i), toolName: long(i) });
    fire("tool_execution_end", { toolCallId: long(i), toolName: long(i) });
  }
  for (let i = 0; i < 40; i++) {
    const id = `s${i}`;
    fire("tool_execution_start", { toolCallId: id, toolName: "subagent" });
    fire("tool_execution_end", {
      toolCallId: id,
      toolName: "subagent",
      result: subagentResult(
        Array.from({ length: 64 }, (_, n) =>
          childRow({ agent: long(n), model: `p/${long(n)}` }),
        ),
      ),
    });
  }
  fire("agent_settled", { aborted: false });
  const turn = sent[0];
  expect(turn.truncated).toBe(true);
  expect(Buffer.byteLength(encoded[0])).toBeLessThanOrEqual(helperLimit);
  expect(encoded[0]).toBe(JSON.stringify(turn));
  // Child rows carry tokens, so tools without children go first, and then
  // each call keeps its first children.
  expect(turn.tools.filter((t) => !t.subagents)).toHaveLength(0);
  const rows = turn.tools.map((t) => t.subagents?.length ?? 0);
  expect(rows).toHaveLength(40);
  expect(Math.min(...rows)).toBeGreaterThan(0);
  expect(Math.max(...rows)).toBeLessThan(64);
});

test("a turn that fits is sent unchanged", () => {
  const turn: Turn = {
    session_id: "s",
    start: "a",
    end: "b",
    status: "done",
    calls: [],
    tools: [],
    truncated: false,
    prompt: "p",
  };
  expect(fit(turn)).toBe(JSON.stringify(turn));
  expect(turn.prompt).toBe("p");
});

// A tool that calls a model itself reports that usage in its result. Pi puts
// the usage of a nested call on that call's own result, so each tool keeps
// what it reported.
test("a tool carries the model usage that it reported", () => {
  const { sent, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "x" });
  fire("tool_execution_start", { toolCallId: "c1", toolName: "codemode" });
  fire("tool_execution_start", {
    toolCallId: "c1/1",
    toolName: "web_search",
    parentToolCallId: "c1",
  });
  fire("tool_execution_end", {
    toolCallId: "c1/1",
    toolName: "web_search",
    result: {
      content: [],
      usage: {
        input: 5,
        output: 7,
        cacheRead: 11,
        cacheWrite: 13,
        totalTokens: 36,
      },
    },
  });
  fire("tool_execution_end", {
    toolCallId: "c1",
    toolName: "codemode",
    result: {
      content: [],
      usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    },
  });
  fire("tool_execution_start", { toolCallId: "c2", toolName: "bash" });
  fire("tool_execution_end", {
    toolCallId: "c2",
    toolName: "bash",
    result: { content: [] },
  });
  fire("agent_settled", { aborted: false });
  expect(sent[0].tools.map((t) => [t.id, t.usage])).toEqual([
    ["c1/1", { input: 5, output: 7, cache_read: 11, cache_write: 13 }],
    ["c1", undefined],
    ["c2", undefined],
  ]);
});

// pi-subagents reports its children's total as the usage of the subagent
// result and of a wait result. The children are already counted.
test("subagent and wait results give no tool usage", () => {
  const usage = { input: 4, output: 460, cacheRead: 5145, cacheWrite: 5211 };
  expect(
    toolUsage("subagent", { ...subagentResult([childRow()]), usage }),
  ).toBeUndefined();
  expect(
    toolUsage("subagent", {
      details: { mode: "single", runId: "r", results: [] },
      usage,
    }),
  ).toBeUndefined();
  expect(
    toolUsage("bg_wait", {
      usage,
      details: { mode: "management", results: [], completions: [{}] },
    }),
  ).toBeUndefined();
  expect(toolUsage("bash", { usage })).toEqual({
    input: 4,
    output: 460,
    cache_read: 5145,
    cache_write: 5211,
  });
});

test("malformed tool usage gives none, never throws", () => {
  for (const result of [
    undefined,
    null,
    "x",
    { usage: null },
    { usage: "x" },
    { usage: { input: -1, output: Number.NaN } },
    {
      get usage() {
        throw new Error("boom");
      },
    },
  ])
    expect(toolUsage("bash", result)).toBeUndefined();
});

// A tool's usage carries tokens, so the tool is kept as long as child rows.
test("a tool with usage outlives tools without tokens when a turn is cut", () => {
  const { sent, encoded, fire } = setup();
  fire("before_agent_start", { messageType: "user", prompt: "p" });
  const long = (i: number) => `${i}-${"x".repeat(250)}`;
  for (let i = 0; i < 400; i++) {
    fire("tool_execution_start", { toolCallId: long(i), toolName: long(i) });
    fire("tool_execution_end", {
      toolCallId: long(i),
      toolName: long(i),
      result: i % 100 === 0 ? { usage: { input: 1, output: 2 } } : {},
    });
  }
  for (let i = 0; i < 40; i++) {
    const id = `s${i}`;
    fire("tool_execution_start", { toolCallId: id, toolName: "subagent" });
    fire("tool_execution_end", {
      toolCallId: id,
      toolName: "subagent",
      result: subagentResult(
        Array.from({ length: 64 }, (_, n) =>
          childRow({ agent: long(n), model: `p/${long(n)}` }),
        ),
      ),
    });
  }
  fire("agent_settled", { aborted: false });
  const turn = sent[0];
  expect(turn.truncated).toBe(true);
  expect(Buffer.byteLength(encoded[0])).toBeLessThanOrEqual(helperLimit);
  expect(turn.tools.filter((t) => t.usage)).toHaveLength(4);
  expect(turn.tools.filter((t) => !t.usage && !t.subagents)).toHaveLength(0);
});
