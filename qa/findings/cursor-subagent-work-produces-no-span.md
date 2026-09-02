# A Cursor sub-agent's work produces no span at all

- **spec.** [cursor/subagents/subagent-work-reaches-no-span](../specs/cursor/subagents/subagent-work-reaches-no-span.md)
- **found.** 2026-09-01, on `qa/runs/probe-cursor-subagent`
- **against.** cursor-agent 2026.08.31-4057e58, plugin working tree at 0.1.26
- **status.** open

## What happens

A Cursor session that delegates produces telemetry for the parent turn and nothing for the
sub-agent. Not a partial span, not an unparented one: nothing.

The recording, in wall-clock order:

```
sessionStart          parent session
beforeSubmitPrompt    parent session
preToolUse   Task     parent session   subagent_type shell, tool_use_id call_…
preToolUse   Shell    sub-agent session, its own fresh UUID
postToolUse  Shell    sub-agent session
afterAgentResponse    parent session
sessionEnd            parent session
```

Dash0 holds one span, the parent's `chat`. `plugin-debug.log` holds one span, so nothing was built
and lost — it was never built.

## Why

Three separate causes, and each is sufficient on its own.

**The `Task` call gets no `postToolUse`.** `internal/pipeline.Process` emits an `execute_tool` span
on `PostToolUse` and `PostToolUseFailure` only, so the delegation itself has no span. On Codex,
`anchorSpawnAgent` turns the spawn call into the anchor a sub-agent's work hangs under; here there is
no anchor because there is no span.

**`subagentStop` never fires.** `internal/source/cursor` maps it to `SubagentStop`, which is the only
event that produces an `invoke_agent` span, and it did not arrive. `subagentStart` is dropped by the
normalizer on purpose; `subagentStop` is the one that matters and Cursor did not send it. Both are
registered — `cursor/hooks.json` lists them and the recorder was registered for all nine events, so
this is Cursor not firing them rather than the plugin not listening.

**The sub-agent's session has no trace context.** It runs under a freshly minted UUID that carries
no `parent_conversation_id` and no field of any kind linking it to the parent, and it fires no
`sessionStart`. So when its `postToolUse` reaches `sendToolTrace`, `LoadTraceContext` finds nothing
and the function returns `no trace context available for tool span`. That error is written to stderr,
which the interactive TUI swallows, so the loss is silent on every channel a user has.

## Why no count-based check catches it

The parent's turn is complete and correct. One `chat` span, correct usage, correct model, and its own
tool calls parented properly. Every column in `qa-compare.py` reconciles, so the run reads as clean.

The harness surfaces it two other ways instead. `qa-session-cursor.sh` identifies the main session by
`sessionStart` and lists every other recorded session in the manifest as `subagent_sessions`;
`qa-compare.py` reads that list and reports a finding when it is non-empty. And the transcript, which
is Cursor's own file and which the plugin never reads, logs the sub-agent's `Shell` call inline — so
`qa-transcript-cursor.py` sees work no span exists for.

## What a fix would need

There is no linking field in any payload, so the plugin cannot correlate the two sessions from what
Cursor gives it today. The options, none of them free:

- **Wait for `subagentStop`.** It exists in Cursor's hook vocabulary and is fired by the server-driven
  dispatcher in the CLI bundle, which is what background-agent and worker modes use. If it fires
  there, the interactive gap may be a Cursor bug worth reporting upstream rather than a plugin one.
- **Mint a conversation per sub-agent session.** Cheap, and it produces token-less conversations with
  no parent — the shape Copilot deliberately rejected for its `call_` sessions.
- **Correlate on time and workspace.** Guessing. Not worth it.

Reporting it upstream is the first step, because two of the three causes are Cursor's.

## Scope

Interactive TUI only, which is the only mode `qa-cursor-drive.py` can drive. Cursor's
background-agent and worker modes dispatch hooks through a different code path and may behave
differently. Nothing here speaks for them, and nothing here speaks for the other three runtimes.
