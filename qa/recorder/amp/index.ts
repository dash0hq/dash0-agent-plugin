// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// QA recorder for the Amp runtime.
//
// The dash0 bridge is a TypeScript plugin that Amp loads in-process, so unlike
// the other four runtimes there is no shell wrapper to intercept and no hook
// payload written to disk. This is the equivalent: a second plugin, bound to
// the same four events, that writes each one verbatim before the bridge acts
// on it. That makes the bridge's input observable, which is what lets a QA run
// tell "the bridge was fed the wrong thing" apart from "the bridge did the
// wrong thing with it" — a distinction that no amount of span reading can make.
//
// It is installed project-scoped, into .amp/plugins/qa-recorder/, so the
// developer's own ~/.config/amp/plugins/ is never touched, and the driver
// removes it again on the way out.
//
// QA_AMP_RECORD names the file. Without it the recorder does nothing at all, so
// an accidentally left-behind copy cannot write into someone's normal session.

import { appendFileSync } from "node:fs";
import type { PluginAPI } from "@ampcode/plugin";

export const description = "QA recorder: writes every Amp plugin event verbatim.";

export default function (amp: PluginAPI) {
  const target = process.env.QA_AMP_RECORD;
  if (!target) return;

  const write = (event: string, payload: unknown) => {
    try {
      appendFileSync(
        target,
        JSON.stringify({ event, at: new Date().toISOString(), payload }) + "\n",
      );
    } catch {
      // A recorder must never be able to break the session it is observing.
    }
  };

  amp.on("agent.start", (event) => {
    write("agent.start", event);
    return {};
  });
  amp.on("tool.call", (event) => {
    write("tool.call", event);
    return { action: "allow" };
  });
  amp.on("tool.result", (event) => {
    write("tool.result", event);
  });
  amp.on("agent.end", (event) => {
    write("agent.end", event);
  });
}
