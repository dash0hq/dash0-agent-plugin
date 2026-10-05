// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

import { Plugin } from "@opencode/plugin";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

// Only these V2 events cross the process boundary. In particular, provider
// headers, credentials, reasoning text and model request bodies never do.
const events = new Set([
  "session.created", "session.deleted", "session.inbox.delivered",
  "session.execution.started", "session.execution.succeeded",
  "session.execution.failed", "session.execution.interrupted",
  "session.step.started", "session.step.ended", "session.step.failed",
  "session.text.ended", "session.tool.input.started", "session.tool.called",
  "session.tool.progress", "session.tool.success", "session.tool.failed",
  "session.skill.activated",
]);

// V2 can overlap location activations during an in-flight reload. Keep ownership
// outside the module cache so a newly loaded revision hands off the old consumer
// before it reads/writes the same correlation file.
/** @type {Map<string, Promise<(() => Promise<void>) | undefined>>} */
const active = globalThis[Symbol.for("dash0.opencode-v2.consumers")] ??= new Map();

// Shipped in the package, so `/dash0-configure` works without a separate install.
const skillPath = fileURLToPath(new URL("skills/dash0-configure/SKILL.md", import.meta.url));

/** @param {import('@opencode/plugin/promise/plugin').Context} ctx */
async function registerSkill(ctx) {
  try {
    const [, front, content] = readFileSync(skillPath, "utf8").match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n([\s\S]*)$/) ?? [];
    const description = front?.match(/^description: '(.*)'\r?$/m)?.[1].replaceAll("''", "'");
    if (!description || content === undefined) throw new Error("unreadable dash0-configure skill");
    const skill = /** @type {any} */ ({ id: "dash0-configure", name: "dash0-configure", description, path: skillPath, content });
    return await ctx.skill.transform((editor) => editor.add(skill));
  } catch (error) {
    // The skill is a convenience; it must never prevent telemetry.
    console.error("dash0 opencode-v2:", error instanceof Error ? error.message : String(error));
  }
}

/** @param {import('@opencode/plugin/promise/plugin').Context} ctx */
export async function setup(ctx) {
  const skill = await registerSkill(ctx);
  const key = JSON.stringify([ctx.location.directory, ctx.location.workspaceID]);
  const previous = active.get(key);
  const started = (async () => {
    await (await previous)?.();
    if (ctx.options.enabled !== false) {
      try {
        return await startConsumer(ctx);
      } catch (error) {
        // A startup failure must not poison subsequent activations. Teardown
        // failures above still prevent replacement of a potentially live writer.
        console.error("dash0 opencode-v2:", error instanceof Error ? error.message : String(error));
      }
    }
  })();
  active.set(key, started);
  const stop = await started;
  if (!stop) {
    if (active.get(key) === started) active.delete(key);
    return skill && (() => skill.dispose());
  }
  return async () => {
    await stop();
    if (active.get(key) === started) active.delete(key);
    await skill?.dispose();
  };
}

/** @param {import('@opencode/plugin/promise/plugin').Context} ctx */
async function startConsumer(ctx) {
  const env = { ...process.env };
  for (const key of ["otlp_url", "auth_token", "auth_token_keychain_service", "auth_token_keychain_account",
    "dataset", "agent_name", "team_name", "omit_io", "omit_user_info", "omit_identity_fallback", "debug", "debug_file"]) {
    const value = ctx.options[key];
    if (value !== undefined) env[`OPENCODE_V2_PLUGIN_OPTION_${key.toUpperCase()}`] = String(value);
  }
  // Reloads in this server/location share state; independent servers and locations
  // must never overwrite it. The binary cache remains shared and versioned.
  env.OPENCODE_V2_PLUGIN_INSTANCE = createHash("sha256")
    .update(`${process.pid}:${ctx.location.directory}:${ctx.location.workspaceID ?? ""}`).digest("hex").slice(0, 16);
  const executable = ctx.options.executable;
  const windows = process.platform === "win32";
  const warn = (/** @type {unknown} */ error) => console.error("dash0 opencode-v2:", error instanceof Error ? error.message : String(error));
  let command = typeof executable === "string" ? executable : "bash";
  let args = typeof executable === "string" ? [] : [fileURLToPath(new URL("opencode-v2-on-event.sh", import.meta.url))];
  if (windows && typeof executable !== "string") {
    // PowerShell verifies/downloads the binary and exits. Spawn Go directly:
    // Windows can then terminate the actual writer, without a taskkill helper
    // or an orphaned descendant continuing to mutate reload state.
    command = await new Promise((resolve) => {
      const resolver = spawn("powershell.exe", ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File",
        fileURLToPath(new URL("opencode-v2-on-event.ps1", import.meta.url)), "--resolve-binary"],
      { cwd: ctx.location.directory, env, stdio: ["ignore", "pipe", "pipe"] });
      // V2 awaits setup behind its activation barrier. A slow download must
      // disable telemetry for this activation, not hold OpenCode back. The
      // resolver keeps going, so the next activation finds the cached binary;
      // its own transfers abort when stalled, and this backstop ends the rest.
      const timer = setTimeout(() => {
        warn("binary resolution is still running; telemetry disabled until the plugin reloads");
        resolve("");
      }, 30_000);
      const backstop = setTimeout(() => resolver.kill("SIGKILL"), 600_000);
      const finish = (/** @type {string} */ path) => { clearTimeout(timer); clearTimeout(backstop); resolve(path); };
      let path = "";
      resolver.stdout.setEncoding("utf8");
      resolver.stdout.on("data", (data) => { path += data; });
      resolver.stderr.on("data", (data) => process.stderr.write(data));
      resolver.once("error", (error) => { warn(error); finish(""); });
      resolver.once("close", (code) => finish(code === 0 ? path.trim() : ""));
    });
    if (!command) return;
    args = [];
  }
  const controller = new AbortController();
  const child = spawn(command, args, { cwd: ctx.location.directory, env, stdio: ["pipe", "ignore", "pipe"] });
  child.stderr.on("data", (data) => process.stderr.write(data));
  child.on("error", warn);
  child.stdin.on("error", warn);
  /** @type {Promise<void> | undefined} */
  let stopping;
  // Register immediately: a failed spawn or an early exporter exit can happen
  // before cleanup. 'close' also waits for inherited wrapper/consumer pipes.
  const closed = new Promise((resolve) => child.once("close", (code) => {
    if (!stopping) {
      // Exit 0 is telemetry turned off (no otlp_url, enabled: false) or a
      // failure that already printed its reason (the bootstrap fails open).
      if (code !== 0) warn("exporter exited; telemetry disabled until the plugin reloads");
      controller.abort();
    }
    resolve(undefined);
  }));

  // Backpressure must not create an unbounded queue or stall OpenCode. Drop a
  // whole event, never a partial JSON line, when the exporter cannot keep up.
  const maxBufferedBytes = 8 * 1024 * 1024;
  // Lifecycle events carry no location; learn session ownership from those that do.
  /** @type {Map<string, boolean>} */
  const owned = new Map();
  const mine = (/** @type {{ directory: string, workspaceID?: string }} */ location) =>
    location.directory === ctx.location.directory
    && ("workspaceID" in location ? location.workspaceID : undefined) === ctx.location.workspaceID;
  const subscription = (async () => {
    try {
      for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
        if (controller.signal.aborted) break;
        if (!events.has(event.type) || !("sessionID" in event.data) || !("created" in event)) continue;
        const sessionID = event.data.sessionID;
        let location = event.location ?? (event.type === "session.created" ? event.data.location : undefined);
        // A workspace-scoped plugin cannot judge a location without workspaceID:
        // it must not disown a session, only follow what is already known, and
        // ask about one it does not know yet (as after a reload).
        // session.created stays authoritative.
        const unscoped = location && event.type !== "session.created" && ctx.location.workspaceID !== undefined
          && !("workspaceID" in location) && location.directory === ctx.location.directory;
        if (unscoped && owned.has(sessionID)) {
          if (owned.get(sessionID) !== true) continue;
        } else if (location && !unscoped) {
          owned.set(sessionID, mine(location));
        } else if (owned.has(sessionID) || event.type === "session.deleted") {
          // A deleted session can no longer be looked up. Forwarding a delete
          // that is not ours is harmless: the exporter drops only state it holds.
          location = { ...ctx.location };
        } else {
          try {
            location = (await ctx.session.get({ sessionID }, { signal: controller.signal })).location;
          } catch (error) {
            warn(error);
            continue;
          }
          // The same exact match as an event location, workspace included: a
          // directory alone cannot tell two instances on it apart, and claiming
          // on it could export the session twice. Cached, so it is asked once.
          owned.set(sessionID, mine(location));
        }
        const foreign = owned.get(sessionID) === false;
        if (event.type === "session.deleted") owned.delete(sessionID);
        if (foreign) continue;
        let prompt;
        if (event.type === "session.inbox.delivered") {
          try {
            const messages = await ctx.session.context({ sessionID: event.data.sessionID }, { signal: controller.signal });
            const message = messages.find((message) => message.id === event.data.inboxID);
            if (message?.type === "user" || message?.type === "synthetic") {
              prompt = { text: message.text, role: message.type === "user" ? "user" : "assistant" };
            }
          } catch (error) {
            warn(error); // Optional IO lookup must not terminate telemetry.
          }
        }
        // Project only consumed fields. V2 payloads can include provider state,
        // raw response bodies and arbitrary metadata, even on these event types.
        const source = /** @type {Record<string, unknown>} */ (event.data);
        /** @type {Record<string, unknown>} */
        const data = {};
        for (const key of ["sessionID", "parentID", "assistantMessageID", "id", "name",
          "agent", "model", "started", "input", "tokens", "reason"]) {
          if (key in source) data[key] = source[key];
        }
        if (event.type === "session.text.ended") data.text = event.data.text;
        if (event.type === "session.tool.progress" && typeof event.data.metadata?.sessionID === "string") {
          data.metadata = { sessionID: event.data.metadata.sessionID };
        }
        if (event.type === "session.tool.success" || event.type === "session.tool.failed") {
          data.content = event.data.content?.filter((part) => part.type === "text")
            .map((part) => ({ type: "text", text: part.text }));
          if (typeof event.data.metadata?.name === "string") data.metadata = { name: event.data.metadata.name };
        }
        if (event.type === "session.tool.failed" || event.type === "session.step.failed"
          || event.type === "session.execution.failed") {
          data.error = { type: event.data.error?.type, message: event.data.error?.message };
        }
        // MCP tools keep V2's flattened <server>_<tool> name. The plugin API
        // cannot tell them from a local tool registered in the same namespace
        // with the same options, so MCP origin is never claimed.
        const line = JSON.stringify({ id: event.id, type: event.type, created: event.created,
          durable: "durable" in event ? event.durable : undefined, location, data, prompt }) + "\n";
        if (controller.signal.aborted || child.stdin.destroyed || child.stdin.writableEnded) continue;
        if (child.stdin.writableLength + Buffer.byteLength(line) > maxBufferedBytes) {
          warn("exporter queue full; dropping event");
          continue;
        }
        child.stdin.write(line);
      }
    } catch (error) {
      if (!controller.signal.aborted) warn(error);
    }
  })();

  return () => stopping ??= (async () => {
    controller.abort();
    await subscription;
    child.stdin.end();
    const timer = setTimeout(() => {
      // A killed bootstrap's curl/wget can keep stderr open; don't wait for it.
      const release = () => child.stderr.destroy();
      if (child.exitCode !== null || child.signalCode !== null) release();
      else child.once("exit", release);
      child.kill("SIGKILL");
    }, 15_000);
    await closed;
    clearTimeout(timer);
  })();
}

export default Plugin.define({ id: "dash0.opencode-v2", setup });
