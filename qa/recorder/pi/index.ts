// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// QA recorder for the Pi runtime.
//
// The dash0 plugin is an extension Pi loads in-process, so there is no hook
// payload on disk to record. This is the equivalent: a second extension, loaded
// before the plugin, that writes every event the plugin consumes verbatim. It
// makes the plugin's input observable, so a run can tell "the plugin was fed
// the wrong thing" from "the plugin did the wrong thing with it".
//
// QA_PI_RECORD names the file. Without it the recorder does nothing, so a copy
// loaded by accident cannot write into someone's normal session.

import { appendFileSync } from "node:fs";
import type {
  ExtensionAPI,
  ExtensionContext,
} from "@earendil-works/pi-coding-agent";

// pi.on is overloaded per event name, so a loop over names needs one loose
// signature. Every event here is notification-only and returns nothing.
type On = (
  name: string,
  handler: (event: unknown, ctx: ExtensionContext) => void,
) => void;

const events = [
  "session_start",
  "before_agent_start",
  "agent_start",
  "message_end",
  "tool_execution_start",
  "tool_execution_end",
  "agent_end",
  "agent_settled",
  "session_shutdown",
] as const;

export default function (pi: ExtensionAPI) {
  const target = process.env.QA_PI_RECORD;
  if (!target) return;
  const on = pi.on.bind(pi) as unknown as On;
  for (const name of events) {
    on(name, (event, ctx) => {
      try {
        // The system prompt is large and says nothing about the plugin's
        // input beyond the skills and cwd, which are kept.
        const payload =
          name === "before_agent_start"
            ? {
                ...(event as object),
                systemPrompt: undefined,
                systemPromptOptions: {
                  cwd: (event as { systemPromptOptions?: { cwd?: string } })
                    .systemPromptOptions?.cwd,
                  skills: (
                    event as {
                      systemPromptOptions?: {
                        skills?: { name: string; filePath: string }[];
                      };
                    }
                  ).systemPromptOptions?.skills?.map((s) => ({
                    name: s.name,
                    filePath: s.filePath,
                  })),
                },
              }
            : event;
        appendFileSync(
          target,
          JSON.stringify({
            event: name,
            at: new Date().toISOString(),
            session: ctx.sessionManager.getSessionId(),
            payload,
          }) + "\n",
        );
      } catch {
        // A recorder must never break the session it observes.
      }
    });
  }
}
