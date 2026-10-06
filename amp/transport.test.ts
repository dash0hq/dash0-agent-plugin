// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import { expect, onTestFinished, test } from "bun:test";
import { execFileSync } from "node:child_process";
import { copyFileSync, mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { delimiter, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import type {
  PluginAPI,
  PluginEventMap,
  PluginHandlerResult,
} from "@ampcode/plugin";

type OTLPValue = { stringValue?: string; intValue?: string };
type OTLPAttribute = { key: string; value: OTLPValue };
type OTLPSpan = {
  name: string;
  spanId: string;
  parentSpanId?: string;
  attributes?: OTLPAttribute[];
};
type OTLPRequest = {
  resourceSpans: Array<{
    scopeSpans: Array<{ spans: OTLPSpan[] }>;
  }>;
};
type Handler<E extends keyof PluginEventMap> = (
  event: PluginEventMap[E],
) => PluginHandlerResult<E>;

test("installed plugin exports lifecycle and per-model usage through its Go helper", async () => {
  const dir = mkdtempSync(join(tmpdir(), "dash0-amp-"));
  onTestFinished(() => rmSync(dir, { recursive: true, force: true }));
  const binDir = join(dir, "bin");
  const homeDir = join(dir, "home");
  mkdirSync(binDir);
  mkdirSync(homeDir);
  const extension = process.platform === "win32" ? ".exe" : "";
  const helper = join(dir, `amp-on-event${extension}`);
  const ampFixture = join(binDir, `amp${extension}`);
  const root = fileURLToPath(new URL("../", import.meta.url));

  // Build before isolating HOME so Go can reuse the developer's module/build cache.
  execFileSync("go", ["build", "-o", helper, "./cmd/amp-on-event"], {
    cwd: root,
  });
  execFileSync("go", ["test", "-c", "-o", ampFixture, "./cmd/amp-on-event"], {
    cwd: root,
  });
  copyFileSync(
    fileURLToPath(new URL("./index.ts", import.meta.url)),
    join(dir, "index.ts"),
  );

  const savedEnvironment = { ...process.env };
  const requests: OTLPRequest[] = [];
  const authorized: boolean[] = [];
  const diagnostics: string[] = [];
  // The turn to reject is identified by a marker in its own payload rather than
  // by a flag the test flips between turns. agent.end hands off after a grace
  // period instead of waiting for the helper to exit, so a flag would be
  // cleared before the helper it was meant for ever sent its request — and the
  // rejection would land on whichever turn happened to be in flight, or on
  // none. 401 is a client error the exporter does not retry, so exactly one
  // request is rejected. OMIT_IO is false here, which is what puts the marker
  // in the body.
  const rejectMarker = "reject-this-turn";
  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    async fetch(request) {
      expect(new URL(request.url).pathname).toBe("/v1/traces");
      authorized.push(
        request.headers.get("Authorization") === "Bearer controlled-test-token",
      );
      const body = await request.text();
      requests.push(JSON.parse(body) as OTLPRequest);
      if (body.includes(rejectMarker))
        return new Response("collector rejected request", { status: 401 });
      return new Response("{}");
    },
  });

  try {
    for (const key of Object.keys(process.env)) {
      if (key.startsWith("AMP_PLUGIN_OPTION_") || key.startsWith("DASH0_")) {
        delete process.env[key];
      }
    }
    process.env.HOME = homeDir;
    process.env.USERPROFILE = homeDir;
    process.env.PATH = `${binDir}${delimiter}${savedEnvironment.PATH ?? ""}`;
    process.env.AMP_PLUGIN_OPTION_OTLP_URL = `http://127.0.0.1:${server.port}`;
    process.env.AMP_PLUGIN_OPTION_AUTH_TOKEN = "controlled-test-token";
    process.env.AMP_PLUGIN_OPTION_EXPORT_USAGE = "true";
    process.env.AMP_PLUGIN_OPTION_OMIT_IO = "false";
    process.env.AMP_PLUGIN_OPTION_OMIT_IDENTITY_FALLBACK = "true";

    const plugin = (await import(pathToFileURL(join(dir, "index.ts")).href))
      .default;
    const handlers = new Map<
      keyof PluginEventMap,
      Handler<keyof PluginEventMap>
    >();
    const mockAPI = {
      on: (
        name: keyof PluginEventMap,
        handler: Handler<keyof PluginEventMap>,
      ) => {
        handlers.set(name, handler);
        return { dispose() {} };
      },
      system: {
        executor: { kind: "remote" },
        workspaceRoot: pathToFileURL(dir),
      },
      helpers: { filePathFromURI: (uri: URL) => fileURLToPath(uri) },
      logger: { log: (message: string) => diagnostics.push(message) },
    } as unknown as PluginAPI;
    plugin(mockAPI);

    const fire = <E extends keyof PluginEventMap>(
      name: E,
      event: PluginEventMap[E],
    ) => handlers.get(name)!(event);
    const thread = { id: "T-test" } as const;
    const finish = async (
      id: "M-a" | "M-b",
      assistantIDs: Array<"M-a" | "M-b"> = [],
      includeTool = false,
      prompt = "private prompt",
    ) => {
      fire("agent.start", {
        thread,
        id,
        message: prompt,
      });
      if (includeTool) {
        fire("tool.call", {
          thread,
          toolUseID: "call",
          tool: "shell_command",
          input: { command: "private command" },
        });
        fire("tool.result", {
          thread,
          toolUseID: "call",
          tool: "shell_command",
          input: { command: "private command" },
          status: "done",
          output: "private output",
        });
      }
      await fire("agent.end", {
        thread,
        id,
        status: "done",
        message: "private response",
        messages: assistantIDs.map((messageID) => ({
          id: messageID,
          role: "assistant",
          content: [{ type: "text", text: "private response" }],
        })),
      });
    };

    process.env.DASH0_TEST_AMP_EXPORT = "ok";
    await finish("M-a", ["M-a", "M-b"], true);

    for (const mode of ["error", "malformed"] as const) {
      process.env.DASH0_TEST_AMP_EXPORT = mode;
      await finish("M-a", ["M-a"]);
    }
    delete process.env.DASH0_TEST_AMP_EXPORT;
    process.env.PATH = homeDir;
    await finish("M-a", ["M-a"]);

    process.env.PATH = `${binDir}${delimiter}${savedEnvironment.PATH ?? ""}`;
    process.env.DASH0_TEST_AMP_EXPORT = "ok";
    await finish("M-a", [], false, rejectMarker);
    await finish("M-b");

    // agent.end no longer waits for the helper to exit: it hands off after a
    // grace period so a usage poll cannot become latency between turns. So
    // delivery is asynchronous, and the test has to wait for it rather than
    // assume that a returned agent.end means the spans have landed.
    const until = async (what: string, ready: () => boolean) => {
      for (let waited = 0; waited < 30000 && !ready(); waited += 50) {
        await Bun.sleep(50);
      }
      expect(
        ready(),
        `timed out waiting for ${what}; requests=${requests.length} diagnostics=${JSON.stringify(diagnostics)}`,
      ).toBe(true);
    };
    await until("6 exports", () => requests.length >= 6);
    await until("the failure diagnostic", () => diagnostics.length >= 1);

    expect(requests).toHaveLength(6);
    expect(authorized).toEqual(Array(6).fill(true));
    expect(diagnostics).toEqual(["dash0: telemetry unavailable"]);
    const spans = requests[0].resourceSpans[0].scopeSpans[0].spans;
    expect(spans).toHaveLength(3);
    expect(spans[0].name).toBe("chat claude-sonnet-4-6");
    expect(spans[2].name).toBe("chat gpt-6-astra");
    for (const span of spans.slice(1)) {
      expect(span.parentSpanId).toBe(spans[0].spanId);
    }
    const toolKeys = (spans[1].attributes ?? []).map(({ key }) => key);
    expect(toolKeys).not.toContain("gen_ai.request.model");
    expect(toolKeys).not.toContain("gen_ai.usage.input_tokens");
    expect(toolKeys).not.toContain("gen_ai.usage.output_tokens");
    const attributes = [spans[2], spans[0]].map((span) =>
      Object.fromEntries(
        (span.attributes ?? []).map(({ key, value }) => [key, value]),
      ),
    );
    expect(attributes[0]["gen_ai.request.model"].stringValue).toBe(
      "gpt-6-astra",
    );
    expect(attributes[0]["gen_ai.usage.input_tokens"].intValue).toBe("41");
    expect(attributes[0]["gen_ai.usage.output_tokens"].intValue).toBe("13");
    expect(attributes[1]["gen_ai.request.model"].stringValue).toBe(
      "claude-sonnet-4-6",
    );
    expect(attributes[1]["gen_ai.usage.input_tokens"].intValue).toBe("71");
    expect(attributes[1]["gen_ai.usage.output_tokens"].intValue).toBe("19");
    for (const request of requests.slice(1, 4)) {
      expect(request.resourceSpans[0].scopeSpans[0].spans).toHaveLength(1);
    }
    // omit_io is false here, so the turn's conversation reaches the exporter.
    const attributesOf = (span: {
      attributes?: { key: string; value: any }[];
    }) =>
      Object.fromEntries(
        (span.attributes ?? []).map(({ key, value }) => [key, value]),
      );
    const root = attributesOf(spans[0]);
    expect(root["gen_ai.input.messages"].stringValue).toContain(
      "private prompt",
    );
    expect(root["gen_ai.output.messages"].stringValue).toContain(
      "private response",
    );
    const tool = attributesOf(spans[1]);
    expect(tool["gen_ai.tool.call.arguments"].stringValue).toContain(
      "private command",
    );
    expect(tool["gen_ai.tool.call.result"].stringValue).toContain(
      "private output",
    );
    expect(JSON.stringify(requests)).not.toContain("controlled-test-token");
  } finally {
    server.stop(true);
    for (const key of Object.keys(process.env)) {
      if (!(key in savedEnvironment)) delete process.env[key];
    }
    Object.assign(process.env, savedEnvironment);
  }
  // Two Go compilations plus one handoff grace per turn. Locally, with a warm
  // build cache, this is ~13 s; CI runs it before any other Go step, so the
  // cache is cold and the compile dominates.
}, 180000);
