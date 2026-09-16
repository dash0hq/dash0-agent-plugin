// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import { expect, test } from "bun:test";
import type {
  PluginAPI,
  PluginEventMap,
  PluginExecutorKind,
} from "@ampcode/plugin";
import { register, type Turn } from "./index";

function setup(executor: PluginExecutorKind = "local") {
  const handlers = new Map<string, (event: unknown) => unknown>();
  const sent: Turn[] = [];
  const api = {
    on: (name: string, handler: (event: unknown) => unknown) =>
      handlers.set(name, handler),
    system: { executor: { kind: executor } },
    logger: { log: () => {} },
  } as unknown as PluginAPI;
  register(api, async (turn) => {
    sent.push(turn);
  });
  return {
    sent,
    fire: <E extends keyof PluginEventMap>(
      name: E,
      event: Partial<PluginEventMap[E]>,
    ) =>
      handlers.get(name)!({ message: "", messages: [], input: {}, ...event }),
    fireRaw: (name: keyof PluginEventMap, event: unknown) =>
      handlers.get(name)!(event),
  };
}

test.each(["local", "remote"] as const)(
  "%s pairs multimodal tools and carries text without image payloads",
  async (executor) => {
    const { sent, fire } = setup(executor);
    const thread = { id: "T-local" } as const;
    expect(fire("agent.start", { thread, id: 7, message: "the ask" })).toEqual(
      {},
    );
    expect(
      fire("tool.call", {
        thread,
        toolUseID: "call",
        tool: "Bash",
        input: { command: "ls" },
      }),
    ).toEqual({ action: "allow" });
    fire("tool.result", {
      thread,
      toolUseID: "call",
      tool: "Bash",
      status: "done",
      output: [
        { type: "text", text: "listing" },
        { type: "image", mimeType: "image/png", data: "SECRET-BASE64" },
        {
          type: "image",
          mimeType: "image/png",
          url: "https://example.invalid/SECRET-IMAGE",
        },
      ],
    });
    await fire("agent.end", {
      thread,
      id: 7,
      status: "done",
      message: "the ask",
      messages: [
        {
          role: "assistant",
          id: 8,
          content: [
            { type: "thinking", thinking: "SECRET-THOUGHT" },
            { type: "text", text: "the answer" },
          ],
        },
      ],
    });
    expect(sent).toHaveLength(1);
    expect(sent[0].executor).toBe(executor);
    expect(sent[0].tools).toHaveLength(1);
    expect(sent[0].assistant_ids).toEqual([8]);
    expect(sent[0].prompt).toBe("the ask");
    expect(sent[0].response).toBe("the answer");
    expect(sent[0].tools[0].input).toBe(JSON.stringify({ command: "ls" }));
    expect(sent[0].tools[0].output).toBe("listing");
    // Image payloads and thinking blocks are dropped before the envelope, so
    // no exporter setting can turn them back on.
    expect(JSON.stringify(sent)).not.toContain("SECRET");
  },
);

test("content is capped per field and per turn", async () => {
  const { sent, fire } = setup("local");
  const thread = { id: "T-big" } as const;
  const huge = "x".repeat(32 * 1024);
  fire("agent.start", { thread, id: 1, message: huge });
  // Twenty 16 KiB outputs cannot all fit in the turn's 256 KiB budget.
  for (let i = 0; i < 20; i++) {
    fire("tool.call", { thread, toolUseID: `c${i}`, tool: "Bash", input: {} });
    fire("tool.result", {
      thread,
      toolUseID: `c${i}`,
      tool: "Bash",
      status: "done",
      output: huge,
    });
  }
  await fire("agent.end", { thread, id: 1, status: "done", messages: [] });
  expect(sent[0].prompt!.length).toBe(16 * 1024);
  expect(sent[0].truncated).toBe(true);
  const total = JSON.stringify(sent[0]).length;
  expect(total).toBeLessThan(1024 * 1024);
});

test("remote threads remain isolated with parallel tools and stale callbacks", async () => {
  const { sent, fire } = setup("remote");
  const a = { id: "T-a" } as const,
    b = { id: "T-b" } as const;
  fire("agent.start", { thread: a, id: "M-a" });
  fire("agent.start", { thread: b, id: "M-b" });
  for (const thread of [a, b]) {
    fire("tool.call", { thread, toolUseID: "call", tool: "shell_command" });
    fire("tool.result", {
      thread,
      toolUseID: "call",
      tool: "shell_command",
      status: "done",
    });
    fire("tool.result", {
      thread,
      toolUseID: "call",
      tool: "shell_command",
      status: "done",
    });
  }
  await fire("agent.end", {
    thread: b,
    id: "M-b",
    status: "cancelled",
    messages: [],
  });
  fire("agent.start", { thread: b, id: "M-new" });
  await fire("agent.end", {
    thread: b,
    id: "M-b",
    status: "done",
    messages: [],
  });
  fire("tool.result", {
    thread: b,
    toolUseID: "call",
    tool: "shell_command",
    status: "done",
  });
  await fire("agent.end", {
    thread: a,
    id: "M-a",
    status: "done",
    messages: [],
  });
  await fire("agent.end", {
    thread: b,
    id: "M-new",
    status: "done",
    messages: [],
  });
  expect(sent.map((t) => [t.thread_id, t.executor, t.tools.length])).toEqual([
    ["T-b", "remote", 1],
    ["T-a", "remote", 1],
    ["T-b", "remote", 0],
  ]);
});

test("typed turn IDs and duplicate starts preserve the active turn", async () => {
  const { sent, fire } = setup();
  const thread = { id: "T-test" } as const;
  fire("agent.start", { thread, id: 42 });
  fire("tool.call", { thread, toolUseID: "call", tool: "Bash" });
  fire("agent.start", { thread, id: 42 });
  fire("tool.result", {
    thread,
    toolUseID: "call",
    tool: "Bash",
    status: "done",
  });
  await fire("agent.end", { thread, id: "42", status: "done" });
  expect(sent).toHaveLength(0);
  await fire("agent.end", { thread, id: 42, status: "done" });
  await fire("agent.end", { thread, id: 42, status: "done" });
  expect(sent).toHaveLength(1);
  expect(sent[0].tools).toHaveLength(1);
});

test("reload and malformed events never fabricate a start or alter tool behavior", async () => {
  const { sent, fireRaw: fire } = setup();
  for (const payload of [null, {}, { thread: { id: "T-test" }, id: null }]) {
    expect(fire("agent.start", payload)).toEqual({});
    expect(fire("tool.call", payload)).toEqual({ action: "allow" });
    fire("tool.result", payload);
    await fire("agent.end", payload);
  }
  await fire("agent.end", {
    thread: { id: "T-test" },
    id: "M-unobserved",
    status: "done",
  });
  expect(sent).toHaveLength(0);
});

test("bounded turns mark truncation and omit excess tools and IDs", async () => {
  const { sent, fire } = setup();
  const thread = { id: "T-test" } as const;
  fire("agent.start", { thread, id: 1 });
  for (let i = 0; i < 513; i++) {
    fire("tool.call", { thread, toolUseID: String(i), tool: "Bash" });
    fire("tool.result", {
      thread,
      toolUseID: String(i),
      tool: "Bash",
      status: "done",
    });
  }
  await fire("agent.end", {
    thread,
    id: 1,
    status: "done",
    messages: Array.from({ length: 513 }, (_, id) => ({
      id,
      role: "assistant",
      content: [],
    })),
  });
  expect(sent[0].tools).toHaveLength(512);
  expect(sent[0].assistant_ids).toHaveLength(512);
  expect(sent[0].truncated).toBe(true);
});

test("a failed exporter does not poison subsequent turns", async () => {
  const handlers = new Map<string, (event: unknown) => unknown>();
  let calls = 0;
  register(
    {
      on: (n: string, h: (event: unknown) => unknown) => handlers.set(n, h),
      system: { executor: { kind: "unknown" } },
      logger: { log: () => {} },
    } as unknown as PluginAPI,
    async () => {
      calls++;
      throw Error("SECRET");
    },
  );
  for (const id of [1, 2]) {
    handlers.get("agent.start")!({ thread: { id: "T-test" }, id });
    await handlers.get("agent.end")!({
      thread: { id: "T-test" },
      id,
      status: "done",
    });
  }
  expect(calls).toBe(2);
});

test("delivery capacity is released after success and failure", async () => {
  const handlers = new Map<string, (event: unknown) => unknown>();
  const pending: { resolve: () => void; reject: () => void }[] = [];
  const logs: string[] = [];
  register(
    {
      on: (n: string, h: (event: unknown) => unknown) => handlers.set(n, h),
      system: { executor: { kind: "remote" } },
      logger: { log: (s: string) => logs.push(s) },
    } as unknown as PluginAPI,
    () =>
      new Promise<void>((resolve, reject) => pending.push({ resolve, reject })),
  );
  const finish = (id: number) => {
    const event = { thread: { id: `T-${id}` }, id, status: "done" };
    handlers.get("agent.start")!(event);
    return handlers.get("agent.end")!(event);
  };
  const deliveries = [0, 1, 2, 3].map(finish);
  await finish(4);
  expect(pending).toHaveLength(4);
  expect(logs).toEqual(["dash0: turn dropped, exporter busy"]);
  pending[0].resolve();
  pending[1].reject();
  await Promise.all(deliveries.slice(0, 2));
  const later = [finish(0), finish(1)];
  expect(pending).toHaveLength(6);
  for (const p of pending.slice(2)) p.resolve();
  await Promise.all([...deliveries, ...later]);
  expect(logs).toEqual([
    "dash0: turn dropped, exporter busy",
    "dash0: telemetry unavailable",
  ]);
});

test("an array mixing blocks with unknown elements still drops images", async () => {
  const { sent, fire } = setup("local");
  const thread = { id: "T-mixed" } as const;
  fire("agent.start", { thread, id: 1, message: "the ask" });
  fire("tool.call", { thread, toolUseID: "call", tool: "Bash", input: {} });
  fire("tool.result", {
    thread,
    toolUseID: "call",
    tool: "Bash",
    status: "done",
    output: [
      { type: "text", text: "listing" },
      { type: "image", mimeType: "image/png", data: "SECRET-BASE64" },
      null,
      { missingType: true },
    ],
  });
  await fire("agent.end", { thread, id: 1, status: "done", messages: [] });
  expect(sent[0].tools[0].output).toBe("listing");
  // One odd element must not send the whole array through JSON.stringify.
  expect(JSON.stringify(sent)).not.toContain("SECRET");
});

test("a clipped response still reports the turn as truncated", async () => {
  const { sent, fire } = setup("local");
  const thread = { id: "T-clip" } as const;
  fire("agent.start", { thread, id: 1, message: "the ask" });
  await fire("agent.end", {
    thread,
    id: 1,
    status: "done",
    messages: [
      {
        role: "assistant",
        id: 2,
        content: [{ type: "text", text: "y".repeat(32 * 1024) }],
      },
    ],
  });
  expect(sent[0].response!.length).toBe(16 * 1024);
  expect(sent[0].truncated).toBe(true);
});
