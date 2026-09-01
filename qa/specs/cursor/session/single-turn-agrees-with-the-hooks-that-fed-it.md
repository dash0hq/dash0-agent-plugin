---
id: single-turn-agrees-with-the-hooks-that-fed-it
area: cursor/session
runtime: cursor
status: active
input: qa/tools/qa-session-cursor.sh, one turn with one shell tool call
duration: ~40s
settling: 25s
cleanup: keep
covers:
  - internal/source/cursor/cursor.go
  - internal/pipeline/pipeline.go
  - cmd/cursor-on-event/main.go
---

## Given

The Cursor registration this machine already has, pointed at the target in
`qa/config.local.json` for the duration of the run through `CURSOR_PLUGIN_OPTION_*`, plus the QA
recorder registered at project scope. Nothing on the machine is mutated: `DASH0_PLUGIN_DATA` moves
the binary cache and the session state into the run directory, and the recorder lives in the scratch
project rather than in `~/.cursor`. See `### Cursor` in [../../../setup.md](../../../setup.md).

**The driver types into a terminal.** `cursor-agent -p` fires no `afterAgentResponse`, so a
print-mode session has no turn to assert anything about. The interactive TUI fires the full set and
`qa/tools/qa-cursor-drive.py` drives it.

**One turn, one tool call.** The tool call is what makes the parenting claim meaningful: the
`execute_tool` span has to hang under the turn's `chat` span, and with no tool call there is nothing
to parent.

**The registered wrapper must be the one this checkout ships**, and the driver refuses otherwise.
A wrapper from before 0.1.25 re-exports `CURSOR_PLUGIN_OPTION_AUTH_TOKEN` from the user's own config
file, which overwrites the QA token; see
[cursor-stale-wrapper-overwrites-the-qa-token](../../../learnings/cursor-stale-wrapper-overwrites-the-qa-token.md).

## When

```sh
QA_CURSOR_BINARY=working-tree qa/tools/qa-session-cursor.sh \
  'Run the shell command: echo qa-probe. Then reply with exactly the word done.' \
  spec-cursor-single-turn
sleep 25
```

Shape, measured 2026-09-01 on `qa/runs/setup-probe-cursor-attrs` against cursor-agent
2026.08.31-4057e58 and the working tree as v0.1.26: 6 hook invocations — `sessionStart`,
`beforeSubmitPrompt`, `preToolUse`, `postToolUse`, `afterAgentResponse`, `sessionEnd` — 3 distinct
transcript snapshots, 2 spans in Dash0, and `"spans_logged": 2` in the manifest.

## Expectation

**The recording alone implies every span**, which is what this runtime shares with Claude and Codex
and Copilot does not. `internal/source/cursor.Normalize` renames Cursor's lowerCamel names into the
pipeline's vocabulary, and the mapping is one hook to one span:

| recorded event | becomes | span |
| --- | --- | --- |
| `afterAgentResponse` | `Stop` | one `chat` |
| `postToolUse` | `PostToolUse` | one `execute_tool` |
| `postToolUseFailure` | `PostToolUseFailure` | one `execute_tool`, ERROR |
| `subagentStop` | `SubagentStop` | one `invoke_agent` |

`preToolUse`, `sessionStart` and `sessionEnd` imply no span. `subagentStart` is dropped by the
normalizer outright.

**The turn's usage is what the `afterAgentResponse` payload carried.** On the reference run: 32283
input, 81 output, 10752 cache-read, 0 cache-write. This is a faithful-copy claim and nothing more —
the payload is the plugin's input, and no other channel on this runtime holds a token count.

**The model is `cursor-auto`, not `default`.** Cursor reports `model: "default"` when the picker is
on Auto, and `Normalize` rewrites it so a dashboard can tell auto-routing from a vendor whose name
happens to be "default". The rewrite is the assertable claim; which model actually served the
request is not observable at all.

**The tool span's duration comes from the payload's `duration`, in milliseconds.** `Normalize` moves
it to `duration_ms`, which the pipeline reads. A `postToolUse` is a single point in time, so without
that field the span would have no duration to reconstruct.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-cursor-single-turn`, which reads spans
  back filtered to `gen_ai.conversation.id` and lines them up against the recording.
- The hook record: `record/index.jsonl` and `record/events/*.json`, written by a separate process
  that reads nothing the plugin wrote.
- Channel two, the transcript: `qa/tools/qa-transcript-cursor.py qa/runs/spec-cursor-single-turn`.
  It corroborates the turn count and nothing numeric. Its tool count is a superset in another
  vocabulary; see [../README.md](../README.md).
- `plugin-debug.log` splits one failure in two. A span there but not in Dash0 was built and lost in
  transport or ingest; a span in neither was never built. That is what distinguished a stale-wrapper
  401 from a pipeline defect on the first run of this spec.

## Then

- Dash0 holds exactly 2 spans for the session: 1 `chat`, 1 `execute_tool`.
- The `execute_tool` span's `parentSpanId` is the `chat` span's id, and the `chat` span has no
  parent. `qa-compare.py` prints `every span's parent is a span of this session`.
- `gen_ai.tool.name` is `Shell`, and `gen_ai.tool.call.id` is the payload's `tool_use_id`.
- The `execute_tool` span's duration is non-zero and within a second of the payload's `duration`.
- `gen_ai.request.model` is `cursor-auto` on both spans.
- `service.name`, `gen_ai.agent.name` and `gen_ai.harness.name` are all `cursor`.
- `dash0.team.name` is `dash0-qa`, which the driver set — its presence proves the QA
  configuration reached the plugin rather than the machine's own file.
- The `chat` span's `gen_ai.usage.*` equal the `afterAgentResponse` payload's four token fields.
- The transcript reports 1 submitted prompt, matching the one `chat` span.
- `qa-compare.py` exits `0`.

## Tolerance

**Token counts vary between runs**, and the numbers above are the measured shape rather than
constants. Only the equality between the payload and the span is asserted.

**How many tool calls the model makes is not fixed.** One shell command is what the prompt asks for;
a model that greps around first makes more, and the assertion is the agreement between the columns,
not the count. A prompt run inside a checkout invites wandering — measured on
`qa/runs/probe-cursor-mcp`, 11 tool calls for a one-call prompt — so read the tool table before
calling a count wrong.

**`cache_write` is 0 rather than absent.** Cursor sends `cache_write_tokens: 0`, and `moveInt64`
copies it, so `gen_ai.usage.cache_creation.input_tokens` is present and zero. Absent would be the
finding.

**The transcript's `turn_ended` marker is not a turn count.** It fires once per agent loop. See
[cursor-turn-ended-is-per-loop-not-per-turn](../../../learnings/cursor-turn-ended-is-per-loop-not-per-turn.md).

**Ingest lag: allow 25 seconds, not 8.** The measurement behind that is in `## Settling` in
[../../../setup.md](../../../setup.md); it was taken on Copilot and the ingest path is shared.
