// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { test } from "node:test";
import childProcess from "node:child_process";
import { syncBuiltinESMExports } from "node:module";
import { EventEmitter } from "node:events";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import plugin, { setup } from "./index.js";

const spawnReal = childProcess.spawn;

function fixture(t, events, overrides = {}) {
  const lines = [];
  const child = new EventEmitter();
  child.exitCode = child.signalCode = null;
  child.stderr = new EventEmitter();
  child.stdin = new EventEmitter();
  child.stdin.writableLength = 0;
  child.stdin.write = (line) => { lines.push(JSON.parse(line)); return true; };
  child.stdin.end = () => { child.exitCode = 0; child.emit("exit", 0); child.emit("close", 0); };
  child.kill = () => { throw new Error("graceful cleanup should not kill the exporter"); };
  const spawn = t.mock.method(childProcess, "spawn", () => child);
  syncBuiltinESMExports();
  t.after(() => { spawn.mock.restore(); syncBuiltinESMExports(); });
  const warnings = t.mock.method(console, "error", () => {});
  let drained;
  const consumed = new Promise((resolve) => { drained = resolve; });
  let aborted = false;
  const skills = [];
  const ctx = {
    options: { executable: "/test/exporter", auth_token: "test-token", omit_io: false },
    location: { directory: "/project" },
    session: {
      get: async ({ sessionID }) => ({ location: { directory: sessionID === "other" ? "/other" : "/project" } }),
      context: async () => [{ id: "inbox1", type: "user", text: "my prompt" }],
      ...overrides,
    },
    skill: {
      transform: async (callback) => {
        const added = [];
        callback({ add: (skill) => added.push(skill) });
        const registration = { added, disposed: false, dispose: async () => { registration.disposed = true; } };
        skills.push(registration);
        return registration;
      },
    },
    event: {
      subscribe: async function* ({ signal }) {
        try {
          for (const event of events) yield event;
          drained();
          if (!signal.aborted) await new Promise((resolve) => signal.addEventListener("abort", resolve, { once: true }));
        } finally {
          aborted = signal.aborted;
        }
      },
    },
  };
  return { ctx, lines, child, consumed, spawn, warnings, skills, wasAborted: () => aborted };
}

function event(type, data = {}, directory = "/project") {
  return { type, id: "evt1", created: 1000, location: directory ? { directory } : undefined,
    data: { sessionID: "owned", ...data } };
}

test("V2 entrypoint filters locations and resolves locationless execution events", async (t) => {
  assert.equal(plugin.id, "dash0.opencode-v2");
  const f = fixture(t, [
    event("session.created"),
    event("session.execution.started", {}, null),
    event("session.execution.started", { sessionID: "other" }, null),
    event("session.step.started", {}, "/other"),
    event("session.reasoning.ended", { text: "private reasoning" }),
    event("session.inbox.delivered", { inboxID: "inbox1" }),
    event("session.tool.progress", { metadata: { sessionID: "child", private: "do not forward" } }),
    event("session.execution.succeeded", {}, null),
  ]);
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.deepEqual(f.lines.map((line) => line.type), ["session.created", "session.execution.started", "session.inbox.delivered", "session.tool.progress", "session.execution.succeeded"]);
  assert.equal(f.lines[1].location.directory, "/project");
  assert.deepEqual(f.lines[2].prompt, { text: "my prompt", role: "user" });
  assert.deepEqual(f.lines[3].data.metadata, { sessionID: "child" });
  assert.equal(f.wasAborted(), true);
  const [command, args, options] = f.spawn.mock.calls[0].arguments;
  assert.equal(command, "/test/exporter");
  assert.deepEqual(args, []);
  assert.equal(options.cwd, "/project");
  assert.equal(options.env.OPENCODE_V2_PLUGIN_OPTION_AUTH_TOKEN, "test-token");
  assert.equal(options.env.OPENCODE_V2_PLUGIN_OPTION_OMIT_IO, "false");
  assert.match(options.env.OPENCODE_V2_PLUGIN_INSTANCE, /^[0-9a-f]{16}$/);
});

test("workspace-scoped plugins claim a locationless session only through a matching workspace", async (t) => {
  const f = fixture(t, [
    event("session.execution.started", {}, null),
    event("session.execution.started", { sessionID: "ws-session" }, null),
    event("session.text.ended", { sessionID: "ws-session" }, null),
    { ...event("session.step.started"), location: { directory: "/project", workspaceID: "ws1" } },
    { ...event("session.step.ended"), location: { directory: "/project", workspaceID: "ws2" } },
    event("session.text.ended"),
  ], { get: async ({ sessionID }) => ({ location: sessionID === "ws-session"
    ? { directory: "/project", workspaceID: "ws1" } : { directory: "/project" } }) });
  f.ctx.location.workspaceID = "ws1";
  const get = t.mock.method(f.ctx.session, "get");
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  // A directory match alone is another instance's session; the decision is cached.
  assert.deepEqual(f.lines.map((line) => [line.type, line.data.sessionID]), [
    ["session.execution.started", "ws-session"], ["session.text.ended", "ws-session"],
    ["session.step.started", "owned"]]);
  assert.equal(get.mock.callCount(), 2);
});

test("a location without workspaceID does not disown a known session", async (t) => {
  const f = fixture(t, [
    { ...event("session.step.started"), location: { directory: "/project", workspaceID: "ws1" } },
    event("session.step.ended"),
    event("session.execution.succeeded", {}, null),
  ]);
  f.ctx.location.workspaceID = "ws1";
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.deepEqual(f.lines.map((line) => line.type),
    ["session.step.started", "session.step.ended", "session.execution.succeeded"]);
  assert.equal(f.lines[2].location.workspaceID, "ws1");
});

test("after a reload, a location without workspaceID asks for the owner of an unknown session", async (t) => {
  // A fresh instance knows no sessions, as after a reload mid-turn.
  const f = fixture(t, [
    event("session.step.started"),
    event("session.step.started", { sessionID: "other" }),
    event("session.step.ended"),
  ], { get: async ({ sessionID }) => ({ location: { directory: "/project",
    workspaceID: sessionID === "owned" ? "ws1" : "ws2" } }) });
  f.ctx.location.workspaceID = "ws1";
  const get = t.mock.method(f.ctx.session, "get");
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.deepEqual(f.lines.map((line) => [line.type, line.data.sessionID]),
    [["session.step.started", "owned"], ["session.step.ended", "owned"]]);
  assert.equal(get.mock.callCount(), 2);
});

test("a delete for an unknown session is forwarded without a lookup", async (t) => {
  // After a reload the instance knows no sessions, and a deleted session can no
  // longer be looked up; the exporter only drops state it actually holds.
  const f = fixture(t, [event("session.deleted", {}, null)],
    { get: async () => { throw new Error("session not found"); } });
  const get = t.mock.method(f.ctx.session, "get");
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.deepEqual(f.lines.map((line) => [line.type, line.data.sessionID]), [["session.deleted", "owned"]]);
  assert.equal(get.mock.callCount(), 0);
  assert.equal(f.warnings.mock.callCount(), 0);
});

test("locationless events follow the owner learned from session.created", async (t) => {
  const f = fixture(t, [
    { ...event("session.created", { sessionID: "foreign", location: { directory: "/project" } }), location: undefined },
    { ...event("session.created", { location: { directory: "/project", workspaceID: "ws1" } }), location: undefined },
    event("session.execution.started", { sessionID: "foreign" }, null),
    event("session.execution.started", {}, null),
  ]);
  f.ctx.location.workspaceID = "ws1";
  const get = t.mock.method(f.ctx.session, "get");
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.deepEqual(f.lines.map((line) => [line.type, line.data.sessionID]),
    [["session.created", "owned"], ["session.execution.started", "owned"]]);
  assert.equal(get.mock.callCount(), 0);
});

test("an exporter that crashes is reported and stops the subscription", async (t) => {
  const f = fixture(t, [event("session.created")]);
  const cleanup = await setup(f.ctx);
  await f.consumed;
  f.child.emit("close", 2);
  await new Promise(setImmediate);
  assert.equal(f.wasAborted(), true);
  assert.match(String(f.warnings.mock.calls.at(-1).arguments[1]), /exporter exited/);
  await cleanup();
});

test("an exporter that exits cleanly stops the subscription quietly", async (t) => {
  // Exit 0 is telemetry turned off (no otlp_url, enabled: false) or a failure
  // whose own reason is already on stderr; neither is a crash.
  const f = fixture(t, [event("session.created")]);
  const cleanup = await setup(f.ctx);
  await f.consumed;
  f.child.emit("close", 0);
  await new Promise(setImmediate);
  assert.equal(f.wasAborted(), true);
  assert.equal(f.warnings.mock.callCount(), 0);
  await cleanup();
});

test("optional session lookups fail without stopping subsequent events", async (t) => {
  const f = fixture(t, [
    event("session.execution.started", {}, null),
    event("session.inbox.delivered", { inboxID: "inbox1" }),
    event("session.execution.succeeded"),
  ], {
    get: async () => { throw new Error("session gone"); },
    context: async () => { throw new Error("context unavailable"); },
  });
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.deepEqual(f.lines.map((line) => line.type), ["session.inbox.delivered", "session.execution.succeeded"]);
  assert.equal(f.warnings.mock.callCount(), 2);
});

test("bounded queue drops whole events and cleanup tolerates a dead exporter", async (t) => {
  const f = fixture(t, [event("session.execution.started")]);
  f.child.stdin.writableLength = 8 * 1024 * 1024;
  const cleanup = await setup(f.ctx);
  await f.consumed;
  f.child.exitCode = 1;
  await cleanup();
  assert.equal(f.lines.length, 0);
  assert.equal(f.warnings.mock.callCount(), 1);
});

test("disabled plugin starts no process but still offers the configure skill", async (t) => {
  const f = fixture(t, []);
  f.ctx.options.enabled = false;
  const cleanup = await setup(f.ctx);
  assert.equal(f.spawn.mock.callCount(), 0);
  const [skill] = f.skills[0].added;
  assert.equal(skill.id, "dash0-configure");
  assert.match(skill.description, /^Configure the Dash0 → OpenCode V2 telemetry integration/);
  assert.match(skill.path, /skills[\\/]dash0-configure[\\/]SKILL\.md$/);
  assert.match(skill.content, /^\n# Configure Dash0 for OpenCode V2/);
  await cleanup();
  assert.equal(f.skills[0].disposed, true);
});

test("a failed skill registration does not block telemetry", async (t) => {
  const f = fixture(t, [event("session.created")]);
  f.ctx.skill.transform = async () => { throw new Error("no skill domain"); };
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.equal(f.lines.length, 1);
  assert.match(f.warnings.mock.calls[0].arguments[1], /no skill domain/);
});

test("tool names pass through unchanged, with no MCP origin claimed", async (t) => {
  const f = fixture(t, [
    event("session.tool.input.started", { name: "qa_under_score_echo_text" }),
    event("session.tool.input.started", { name: "qa_under_score_namespace_custom_echo_text" }),
  ]);
  // An MCP tool and a local tool shaped exactly like it, as OpenCode permits:
  // nothing here distinguishes them, so neither may be attributed to MCP.
  f.ctx.mcp = { list: async () => ({ data: [{ name: "qa_under_score" }] }) };
  f.ctx.tool = { list: async () => [
    { id: "qa_under_score_echo_text", options: { namespace: "qa_under_score", codemode: true }, output: {} },
    { id: "qa_under_score_namespace_custom_echo_text",
      options: { namespace: "qa_under_score", codemode: false }, output: { type: "string" } }] };
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.deepEqual(f.lines.map((line) => [line.data.name, line.mcpServer]), [
    ["qa_under_score_echo_text", undefined],
    ["qa_under_score_namespace_custom_echo_text", undefined]]);
});

test("only selected V2 fields cross into the consumer", async (t) => {
  const e = event("session.tool.failed", {
    error: { type: "ToolError", message: "useful message", response: { body: "PRIVATE_RAW_BODY" } },
    metadata: { token: "PRIVATE_METADATA", name: { private: "PRIVATE_OBJECT" } },
    resultState: { credentials: "PRIVATE_PROVIDER_STATE" },
    content: [{ type: "file", uri: "PRIVATE_FILE" }, { type: "text", text: "tool output" }],
  });
  e.metadata = { private: "PRIVATE_ENVELOPE" };
  const f = fixture(t, [e,
    event("session.tool.progress", { metadata: { sessionID: { credentials: "PRIVATE_PROGRESS" } } }),
    event("session.tool.progress", { metadata: { sessionID: ["PRIVATE_ARRAY"] } }),
  ]);
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.equal(JSON.stringify(f.lines).includes("PRIVATE_"), false);
  assert.deepEqual(f.lines[0].data.error, { type: "ToolError", message: "useful message" });
  assert.deepEqual(f.lines[0].data.content, [{ type: "text", text: "tool output" }]);
});

test("events missing optional fields do not end the subscription", async (t) => {
  const f = fixture(t, [
    event("session.tool.progress"),
    event("session.tool.failed"),
    event("session.tool.input.started"),
    event("session.execution.succeeded"),
  ]);
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.deepEqual(f.lines.map((line) => line.type), ["session.tool.progress", "session.tool.failed",
    "session.tool.input.started", "session.execution.succeeded"]);
});

test("forced cleanup kills the directly owned consumer and waits for close", async (t) => {
  const platform = Object.getOwnPropertyDescriptor(process, "platform");
  Object.defineProperty(process, "platform", { value: "win32" });
  t.after(() => Object.defineProperty(process, "platform", platform));
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const f = fixture(t, []);
  f.child.pid = 12345;
  f.child.stdin.end = () => {};
  const cleanup = await setup(f.ctx);
  await f.consumed;
  const kill = t.mock.method(f.child, "kill", () => true);
  let cleaned = false;
  const result = cleanup().then(() => { cleaned = true; });
  await new Promise(setImmediate);
  t.mock.timers.tick(15_000);
  assert.deepEqual(kill.mock.calls[0].arguments, ["SIGKILL"]);
  await new Promise(setImmediate);
  assert.equal(cleaned, false); // Issuing a signal is not proof the child closed.
  // A killed bootstrap's download can hold stderr open; release it on exit.
  let released = false;
  f.child.stderr.destroy = () => { released = true; };
  f.child.emit("exit", null, "SIGKILL");
  assert.equal(released, true);
  f.child.emit("close", 1);
  await result;
  assert.equal(cleaned, true);
});

test("cleanup handles an exporter that failed to spawn before reload", async (t) => {
  const f = fixture(t, []);
  const cleanup = await setup(f.ctx);
  await f.consumed;
  f.child.emit("error", new Error("missing executable"));
  f.child.emit("close", -1);
  await cleanup();
  assert.equal(f.warnings.mock.callCount(), 2); // spawn error, then the exit notice
});

// The mocked tests above cannot catch quoting, encoding or path bugs between
// Node and the real PowerShell bootstrap; only windows-latest CI runs this.
test("Windows resolves a cached binary through the real PowerShell bootstrap", { skip: process.platform !== "win32" }, async (t) => {
  const data = mkdtempSync(path.join(tmpdir(), "dash0 café-"));
  t.after(() => rmSync(data, { recursive: true, force: true }));
  const version = readFileSync(new URL("opencode-v2-on-event.ps1", import.meta.url), "utf8").match(/^\$Version = '(.+)'$/m)[1];
  const arch = process.arch === "arm64" ? "arm64" : "amd64";
  const binary = path.join(data, "bin", `opencode-v2-on-event-${version}-windows-${arch}.exe`);
  mkdirSync(path.dirname(binary));
  writeFileSync(binary, ""); // A cached binary is not re-verified, and the consumer spawn is mocked.
  const previous = process.env.OPENCODE_V2_PLUGIN_DATA;
  process.env.OPENCODE_V2_PLUGIN_DATA = data;
  t.after(() => { if (previous === undefined) delete process.env.OPENCODE_V2_PLUGIN_DATA; else process.env.OPENCODE_V2_PLUGIN_DATA = previous; });
  const f = fixture(t, []);
  delete f.ctx.options.executable;
  f.ctx.location.directory = data;
  f.spawn.mock.mockImplementation((command, args, options) => command === "powershell.exe"
    ? spawnReal(command, args, options) : f.child);
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.equal(path.resolve(f.spawn.mock.calls[1].arguments[0]), binary);
  assert.equal(f.warnings.mock.callCount(), 0);
});

test("Windows resolves the verified path then directly spawns the consumer", async (t) => {
  const platform = Object.getOwnPropertyDescriptor(process, "platform");
  Object.defineProperty(process, "platform", { value: "win32" });
  t.after(() => Object.defineProperty(process, "platform", platform));
  const f = fixture(t, []);
  delete f.ctx.options.executable;
  const resolver = new EventEmitter();
  resolver.stdout = new EventEmitter();
  resolver.stdout.setEncoding = () => {};
  resolver.stderr = new EventEmitter();
  f.spawn.mock.mockImplementation((command) => {
    if (command !== "powershell.exe") return f.child;
    setImmediate(() => {
      resolver.stdout.emit("data", "C:\\Users\\café\\opencode.exe\r\n");
      resolver.emit("close", 0);
    });
    return resolver;
  });
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.equal(f.spawn.mock.calls[0].arguments[1].at(-1), "--resolve-binary");
  assert.equal(f.spawn.mock.calls[1].arguments[0], "C:\\Users\\café\\opencode.exe");
  assert.deepEqual(f.spawn.mock.calls[1].arguments[1], []);
});

test("a failed Windows resolver does not subscribe or spawn a consumer", async (t) => {
  const platform = Object.getOwnPropertyDescriptor(process, "platform");
  Object.defineProperty(process, "platform", { value: "win32" });
  t.after(() => Object.defineProperty(process, "platform", platform));
  const f = fixture(t, []);
  delete f.ctx.options.executable;
  const resolver = new EventEmitter();
  resolver.stdout = new EventEmitter();
  resolver.stdout.setEncoding = () => {};
  resolver.stderr = new EventEmitter();
  f.spawn.mock.mockImplementation(() => {
    setImmediate(() => resolver.emit("error", new Error("PowerShell unavailable")));
    return resolver;
  });
  await setup(f.ctx);
  assert.equal(f.spawn.mock.callCount(), 1);
});

test("a slow Windows resolver stops blocking activation but keeps downloading", async (t) => {
  const platform = Object.getOwnPropertyDescriptor(process, "platform");
  Object.defineProperty(process, "platform", { value: "win32" });
  t.after(() => Object.defineProperty(process, "platform", platform));
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const f = fixture(t, []);
  delete f.ctx.options.executable;
  const resolver = new EventEmitter();
  resolver.stdout = new EventEmitter();
  resolver.stdout.setEncoding = () => {};
  resolver.stderr = new EventEmitter();
  const kill = resolver.kill = t.mock.fn(() => true);
  f.spawn.mock.mockImplementation(() => resolver);
  const activation = setup(f.ctx);
  await new Promise(setImmediate);
  t.mock.timers.tick(30_000);
  await activation; // Never emits close, even on kill.
  assert.equal(kill.mock.callCount(), 0); // Still downloading for the next activation.
  t.mock.timers.tick(570_000);
  assert.deepEqual(kill.mock.calls[0].arguments, ["SIGKILL"]);
  assert.equal(f.spawn.mock.callCount(), 1);
  assert.match(f.warnings.mock.calls[0].arguments[1], /resolution is still running/);
  f.ctx.options.executable = "/test/exporter";
  f.spawn.mock.mockImplementation(() => f.child);
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
});

test("a synchronous startup failure does not poison subsequent reloads", async (t) => {
  const f = fixture(t, []);
  f.spawn.mock.mockImplementation(() => { throw new Error("invalid executable"); });
  await setup(f.ctx);
  assert.equal(f.warnings.mock.callCount(), 1);
  f.spawn.mock.mockImplementation(() => f.child);
  const cleanup = await setup(f.ctx);
  await f.consumed;
  await cleanup();
  assert.equal(f.spawn.mock.callCount(), 2);
});

test("overlapping V2 activations hand off ownership before starting a replacement", async (t) => {
  const old = fixture(t, []);
  const cleanupOld = await setup(old.ctx);
  await old.consumed;
  old.child.stdin.end = () => {};
  old.spawn.mock.restore();
  syncBuiltinESMExports();
  const next = fixture(t, [event("session.execution.succeeded")]);
  const replacement = setup(next.ctx);
  await new Promise(setImmediate);
  assert.equal(old.wasAborted(), true);
  assert.equal(next.spawn.mock.callCount(), 0);
  old.child.emit("close", 0);
  const cleanupNext = await replacement;
  await next.consumed;
  await cleanupOld(); // Old V2 scope may close after its replacement activates.
  assert.equal(next.child.exitCode, null);
  assert.equal(next.lines.length, 1);
  await cleanupNext();
});
