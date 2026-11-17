// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import { expect, onTestFinished, test } from "bun:test";
import { execFileSync } from "node:child_process";
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

type OTLPValue = { stringValue?: string; intValue?: string };
type OTLPSpan = {
  name: string;
  spanId: string;
  parentSpanId?: string;
  attributes?: Array<{ key: string; value: OTLPValue }>;
};
type OTLPRequest = {
  resourceSpans: Array<{ scopeSpans: Array<{ spans: OTLPSpan[] }> }>;
};
type Handler = (event: unknown, ctx: unknown) => unknown;

const attributesOf = (span: OTLPSpan): Record<string, OTLPValue> =>
  Object.fromEntries(
    (span.attributes ?? []).map(({ key, value }) => [key, value]),
  );

/** Load a fresh copy of index.ts from dir with a mock Pi, and drive it. */
async function load(dir: string) {
  const handlers = new Map<string, Handler[]>();
  const notices: string[] = [];
  const api = {
    on: (name: string, handler: Handler) =>
      handlers.set(name, [...(handlers.get(name) ?? []), handler]),
  } as unknown as ExtensionAPI;
  const extension = (await import(pathToFileURL(join(dir, "index.ts")).href))
    .default;
  extension(api);
  const ctx = {
    cwd: dir,
    hasUI: true,
    ui: { notify: (message: string) => notices.push(message) },
    sessionManager: {
      getSessionId: () => "019a-transport",
      getHeader: () => ({}),
      getSessionFile: () => undefined,
    },
    modelRegistry: {
      isUsingOAuth: (m: { provider: string }) => m.provider === "claude-code",
      getProvider: () => ({ auth: { oauth: { isSubscription: true } } }),
    },
  };
  const fire = async (name: string, event: Record<string, unknown> = {}) => {
    for (const handler of handlers.get(name) ?? [])
      await handler({ type: name, ...event }, ctx);
  };
  return { fire, notices, ctx };
}

/**
 * Copy index.ts into dir. Pi maps its own packages for an extension; here
 * the extension's node_modules supplies them.
 */
function install(dir: string) {
  copyFileSync(
    fileURLToPath(new URL("./index.ts", import.meta.url)),
    join(dir, "index.ts"),
  );
  symlinkSync(
    fileURLToPath(new URL("./node_modules", import.meta.url)),
    join(dir, "node_modules"),
    "junction",
  );
}

const saved = { ...process.env };
function isolate(home: string) {
  for (const key of Object.keys(process.env))
    if (key.startsWith("PI_") || key.startsWith("DASH0_"))
      delete process.env[key];
  process.env.HOME = home;
  process.env.USERPROFILE = home;
  onTestFinished(() => {
    for (const key of Object.keys(process.env)) delete process.env[key];
    Object.assign(process.env, saved);
  });
}

async function turn(
  fire: (name: string, e?: Record<string, unknown>) => Promise<void>,
) {
  await fire("session_start", { reason: "startup" });
  await fire("before_agent_start", {
    messageType: "user",
    prompt: "private prompt",
  });
  await fire("message_end", {
    message: {
      role: "assistant",
      provider: "openai",
      model: "gpt-5.5",
      stopReason: "toolUse",
      timestamp: Date.now() - 2000,
      durationMs: 500,
      content: [{ type: "toolCall", id: "call_1", name: "bash" }],
      usage: { input: 10, output: 2, cacheRead: 0, cacheWrite: 0 },
    },
  });
  await fire("tool_execution_start", {
    toolCallId: "call_1",
    toolName: "bash",
    args: { command: "git status" },
  });
  await fire("tool_execution_end", {
    toolCallId: "call_1",
    toolName: "bash",
    isError: false,
    result: { content: [{ type: "text", text: "private output" }] },
  });
  await fire("message_end", {
    message: {
      role: "assistant",
      provider: "claude-code",
      model: "claude-sonnet-5",
      stopReason: "stop",
      timestamp: Date.now() - 1000,
      durationMs: 500,
      content: [{ type: "text", text: "private answer" }],
      usage: { input: 2, output: 40, cacheRead: 300, cacheWrite: 50 },
    },
  });
  await fire("agent_settled", { aborted: false });
}

test("the extension exports a settled turn through its Go helper", async () => {
  const dir = mkdtempSync(join(tmpdir(), "dash0-pi-"));
  onTestFinished(() => rmSync(dir, { recursive: true, force: true }));
  const home = join(dir, "home");
  mkdirSync(home);
  const root = fileURLToPath(new URL("../", import.meta.url));
  const exe = process.platform === "win32" ? ".exe" : "";
  // Built before HOME moves, so Go reuses the developer's build cache.
  execFileSync(
    "go",
    ["build", "-o", join(dir, `pi-on-event${exe}`), "./cmd/pi-on-event"],
    { cwd: root },
  );
  install(dir);

  const requests: OTLPRequest[] = [];
  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    async fetch(request) {
      expect(new URL(request.url).pathname).toBe("/v1/traces");
      expect(request.headers.get("Authorization")).toBe("Bearer test-token");
      requests.push((await request.json()) as OTLPRequest);
      return new Response("{}");
    },
  });
  onTestFinished(() => server.stop(true));
  isolate(home);
  // The user-level file, where an install has it.
  mkdirSync(join(home, ".pi"));
  writeFileSync(
    join(home, ".pi", "dash0-agent-plugin.local.md"),
    `---\notlp_url: http://127.0.0.1:${server.port}\nauth_token: test-token\nomit_identity_fallback: true\n---\n`,
  );

  const { fire, notices } = await load(dir);
  const started = Date.now();
  await turn(fire);
  // The handler hands off; it never waits for the export.
  expect(Date.now() - started).toBeLessThan(1000);
  await fire("session_shutdown", { reason: "quit" });
  for (let i = 0; i < 100 && requests.length === 0; i++) await Bun.sleep(50);

  expect(requests).toHaveLength(1);
  expect(notices).toEqual([]);
  const spans = requests[0].resourceSpans[0].scopeSpans[0].spans;
  expect(spans.map((s) => s.name)).toEqual([
    "chat claude-sonnet-5",
    "chat gpt-5.5",
    "execute_tool bash",
  ]);
  for (const child of spans.slice(1))
    expect(child.parentSpanId).toBe(spans[0].spanId);
  const rootAttrs = attributesOf(spans[0]);
  expect(rootAttrs["gen_ai.harness.name"].stringValue).toBe("pi");
  expect(rootAttrs["gen_ai.provider.name"].stringValue).toBe("anthropic");
  expect(rootAttrs["gen_ai.conversation.id"].stringValue).toBe(
    "019a-transport",
  );
  expect(attributesOf(spans[1])["gen_ai.provider.name"].stringValue).toBe(
    "openai",
  );
  // The claude-code call used a subscription login; the openai one did not.
  expect(rootAttrs["dash0.gen_ai.billing_mode"].stringValue).toBe(
    "subscription",
  );
  expect(attributesOf(spans[1])["dash0.gen_ai.billing_mode"].stringValue).toBe(
    "unknown",
  );
  // omit_io is on by default.
  expect(JSON.stringify(requests)).not.toContain("private");
});

test("a missing helper or a failing export never throws into Pi", async () => {
  const dir = mkdtempSync(join(tmpdir(), "dash0-pi-"));
  onTestFinished(() => rmSync(dir, { recursive: true, force: true }));
  const home = join(dir, "home");
  mkdirSync(home);
  install(dir);
  isolate(home);

  const { fire, notices } = await load(dir);
  await turn(fire);
  await fire("session_shutdown", { reason: "quit" });
  for (let i = 0; i < 40 && notices.length === 0; i++) await Bun.sleep(25);
  expect(notices).toEqual(["dash0: telemetry unavailable"]);
  // Reported once per session, not once per turn.
  await turn(fire);
  await Bun.sleep(100);
  expect(notices).toHaveLength(1);
});

// The npm package ships index.ts and the bootstraps, no helper. The extension
// then runs the bootstrap, which runs the helper it cached on the first turn.
test.skipIf(process.platform === "win32")(
  "without a local helper the bootstrap runs the cached release helper",
  async () => {
    const dir = mkdtempSync(join(tmpdir(), "dash0-pi-"));
    onTestFinished(() => rmSync(dir, { recursive: true, force: true }));
    const home = join(dir, "home");
    mkdirSync(home);
    install(dir);
    copyFileSync(
      fileURLToPath(new URL("./pi-on-event.sh", import.meta.url)),
      join(dir, "pi-on-event.sh"),
    );
    isolate(home);
    const data = join(dir, "data");
    process.env.PI_PLUGIN_DATA = data;
    const version = /^VERSION="(.+)"$/m.exec(
      readFileSync(join(dir, "pi-on-event.sh"), "utf8"),
    )![1];
    const os = process.platform;
    const arch = process.arch === "x64" ? "amd64" : process.arch;
    // A cached helper, so the bootstrap does not download one.
    mkdirSync(join(data, "bin"), { recursive: true });
    const received = join(dir, "received.json");
    writeFileSync(
      join(data, "bin", `pi-on-event-${version}-${os}-${arch}`),
      `#!/bin/sh\ncat >"${received}.tmp" && mv "${received}.tmp" "${received}"\n`,
      { mode: 0o755 },
    );

    const { fire, notices } = await load(dir);
    await turn(fire);
    await fire("session_shutdown", { reason: "quit" });
    for (let i = 0; i < 100 && !existsSync(received); i++) await Bun.sleep(50);

    expect(notices).toEqual([]);
    expect(JSON.parse(readFileSync(received, "utf8")).session_id).toBe(
      "019a-transport",
    );
  },
);

// On Windows, PowerShell only resolves the helper; the extension spawns the
// helper itself, so the envelope's bytes reach it unchanged. Runs the Windows
// branch on a POSIX host, with a stand-in for System32's powershell.exe.
test.skipIf(process.platform === "win32")(
  "on Windows the bootstrap resolves the helper and the extension runs it",
  async () => {
    const dir = mkdtempSync(join(tmpdir(), "dash0-pi-"));
    onTestFinished(() => rmSync(dir, { recursive: true, force: true }));
    const home = join(dir, "home");
    mkdirSync(home);
    install(dir);
    isolate(home);
    const received = join(dir, "received.json");
    const helper = join(dir, "cache", "pi-on-event.exe");
    mkdirSync(join(dir, "cache"));
    writeFileSync(
      helper,
      `#!/bin/sh\ncat >"${received}.tmp" && mv "${received}.tmp" "${received}"\n`,
      { mode: 0o755 },
    );
    const system = join(dir, "System32", "WindowsPowerShell", "v1.0");
    mkdirSync(system, { recursive: true });
    const calls = join(dir, "powershell-args");
    writeFileSync(
      join(system, "powershell.exe"),
      // Fails open with no path the first time, as on a failed download.
      `#!/bin/sh\necho "$@" >>"${calls}"\n[ -e "${calls}.ok" ] && echo "${helper}"\ntouch "${calls}.ok"\n`,
      { mode: 0o755 },
    );
    process.env.SystemRoot = dir;
    const platform = Object.getOwnPropertyDescriptor(process, "platform")!;
    Object.defineProperty(process, "platform", { value: "win32" });
    let loaded;
    try {
      loaded = await load(dir);
    } finally {
      Object.defineProperty(process, "platform", platform);
    }
    const { fire, notices } = loaded;
    const resolutions = () => readFileSync(calls, "utf8").trim().split("\n");

    // The failed resolution loses the turn, with the notice.
    await turn(fire);
    for (let i = 0; i < 100 && notices.length === 0; i++) await Bun.sleep(20);
    expect(notices).toEqual(["dash0: telemetry unavailable"]);
    expect(existsSync(received)).toBe(false);

    // The next turn resolves again and reaches the helper.
    await turn(fire);
    for (let i = 0; i < 100 && !existsSync(received); i++) await Bun.sleep(50);
    const envelope = JSON.parse(readFileSync(received, "utf8"));
    expect(envelope.session_id).toBe("019a-transport");
    expect(resolutions()).toEqual([
      `-NoProfile -NonInteractive -ExecutionPolicy Bypass -File ${join(dir, "pi-on-event.ps1")} --resolve-binary`,
      `-NoProfile -NonInteractive -ExecutionPolicy Bypass -File ${join(dir, "pi-on-event.ps1")} --resolve-binary`,
    ]);

    // A resolved path is kept for later turns.
    rmSync(received);
    await turn(fire);
    for (let i = 0; i < 100 && !existsSync(received); i++) await Bun.sleep(50);
    expect(existsSync(received)).toBe(true);
    expect(resolutions()).toHaveLength(2);
    await fire("session_shutdown", { reason: "quit" });
  },
);

// The extension caps detached helpers. A turn over the cap is not exported,
// so the user gets the same notice as for a failed export.
test.skipIf(process.platform === "win32")(
  "a turn dropped at the helper cap shows the notice",
  async () => {
    const dir = mkdtempSync(join(tmpdir(), "dash0-pi-"));
    onTestFinished(() => rmSync(dir, { recursive: true, force: true }));
    const home = join(dir, "home");
    mkdirSync(home);
    install(dir);
    isolate(home);
    // A helper that succeeds, but only after every turn below is sent.
    writeFileSync(join(dir, "pi-on-event"), "#!/bin/sh\nexec sleep 2\n", {
      mode: 0o755,
    });

    const { fire, notices } = await load(dir);
    for (let i = 0; i < 16; i++) await turn(fire);
    expect(notices).toEqual([]);
    await turn(fire);
    expect(notices).toEqual(["dash0: telemetry unavailable"]);
  },
);

// Pi throws on any use of a session's context once the session has ended, and
// an export fails after that in print mode. The throw would come from a
// child-process callback and end the Pi process. Found live on 2026-11-13.
test("a failing export after the session ended does not crash Pi", async () => {
  const dir = mkdtempSync(join(tmpdir(), "dash0-pi-"));
  onTestFinished(() => rmSync(dir, { recursive: true, force: true }));
  const home = join(dir, "home");
  mkdirSync(home);
  install(dir);
  isolate(home);

  const { fire, ctx } = await load(dir);
  await fire("session_start", { reason: "startup" });
  await fire("session_shutdown", { reason: "quit" });
  let reads = 0;
  Object.defineProperty(ctx, "hasUI", {
    get() {
      reads++;
      throw new Error("This extension ctx is stale");
    },
  });
  const crashes: unknown[] = [];
  const onCrash = (error: unknown) => crashes.push(error);
  process.on("uncaughtException", onCrash);
  onTestFinished(() => process.off("uncaughtException", onCrash));
  // The export of a turn that settles now fails after the shutdown.
  await turn(fire);
  for (let i = 0; i < 40 && reads === 0; i++) await Bun.sleep(25);
  expect(reads).toBe(1);
  expect(crashes).toEqual([]);
});

// Only a quit ends the Pi process. A session switch, fork or reload must not
// wait for a helper that is slow to read its envelope.
test.skipIf(process.platform === "win32")(
  "only a quit waits for envelopes still being written",
  async () => {
    const dir = mkdtempSync(join(tmpdir(), "dash0-pi-"));
    onTestFinished(() => rmSync(dir, { recursive: true, force: true }));
    const home = join(dir, "home");
    mkdirSync(home);
    install(dir);
    isolate(home);
    // A helper that does not read, so an envelope larger than the pipe
    // buffer stays pending.
    writeFileSync(join(dir, "pi-on-event"), "#!/bin/sh\nexec sleep 3\n", {
      mode: 0o755,
    });

    const { fire } = await load(dir);
    await fire("session_start", { reason: "startup" });
    await fire("before_agent_start", { messageType: "user", prompt: "x" });
    for (let i = 0; i < 16; i++) {
      await fire("tool_execution_start", {
        toolCallId: `t${i}`,
        toolName: "bash",
        args: { command: "x".repeat(16_000) },
      });
      await fire("tool_execution_end", {
        toolCallId: `t${i}`,
        toolName: "bash",
      });
    }
    await fire("agent_settled", { aborted: false });
    let started = Date.now();
    await fire("session_shutdown", { reason: "new" });
    expect(Date.now() - started).toBeLessThan(500);
    started = Date.now();
    await fire("session_shutdown", { reason: "quit" });
    expect(Date.now() - started).toBeGreaterThanOrEqual(1500);
  },
);
