---
id: resumed-turn-is-scoped-to-itself
area: copilot/turns
runtime: copilot
status: active
input: qa/tools/qa-session-copilot.sh with QA_COPILOT_RESUME, two turns in one session
duration: ~40s
settling: 25s
cleanup: keep
covers:
  - internal/source/copilot/otelfile.go
  - cmd/copilot-on-event/main.go
  - internal/pipeline/pipeline.go
---

## Given

The plugin installed into a throwaway home by `qa-session-copilot.sh`, exporting to the target in
`qa/config.local.json`, plus the QA recorder. Nothing on the machine is configured or mutated; see
`## Runtimes` in [../../../setup.md](../../../setup.md).

**Two turns, one session.** One `copilot -p` is one turn, so a single-turn run cannot distinguish
"this turn's usage" from "the session's usage": with one turn the two are the same number.
`QA_COPILOT_RESUME` drives the second through `copilot --resume=<id>`, which keeps the session id
and fires a second `sessionStart`, `userPromptSubmitted`, `agentStop` and `sessionEnd`.

**Both turns must call a tool.** A tool span is emitted once, from the file, so a re-read shows up
twice as clearly as a token sum does: the second copy carries the *same native span id* under a
different trace, which no correct run can produce.

**Both launches share one native-OTel file**, because the driver names it once per run. That is
deliberate and it is the harder case. The launch function the `dash0-configure` skill installs gives
each launch its own file and deletes it at exit, so a stale cursor there is harmless — a fresh file
means every span is new anyway. A fixed `COPILOT_OTEL_FILE_EXPORTER_PATH`, which the documentation
offers as the alternative to that function, gives one file for both launches and is where the defect
below was reachable.

## When

```sh
QA_COPILOT_BINARY=working-tree \
QA_COPILOT_RESUME='Now run the shell command: echo qa-second. Then reply with exactly the word done.' \
  qa/tools/qa-session-copilot.sh \
  'Run the shell command: echo qa-first. Then reply with exactly the word done.' \
  spec-copilot-resumed-turn
sleep 25
```

Shape, measured on plugin 0.1.25 with Copilot CLI 1.0.80: 10 hook invocations, 2 `agentStop`, one
native-OTel file holding 2 `invoke_agent`, 4 `chat` and 2 `execute_tool` spans, 4 spans in Dash0,
and `"turns": 2` in the manifest.

## Expectation

**One `chat` span per `agentStop`, from `record/index.jsonl` alone.** Two rows, two spans. The
recorder writes them without reading the OTel file, so this half owes the plugin nothing.

**Each turn's usage is that turn's top-level `invoke_agent` span in the file**, printed per turn by
`qa/tools/qa-otel.py`. Copilot writes one such span per agent turn and sums the turn's round-trips
into it itself, so the partition needs no rule of the plugin's — unlike the Codex arm of this
question, where the harness has to partition by the recorder's `Stop` timestamps because the rollout
has no per-turn record at all.

Measured on the reference run:

| | input | output | cache_read |
| --- | --- | --- | --- |
| turn 1 | 29426 | 88 | 20480 |
| turn 2 | 29684 | 44 | 27648 |
| session | 59110 | 132 | 48128 |

The session row is there to be *contradicted*: before the fix, turn 2's span carried it.

**Each tool call is emitted exactly once**, under the trace of the turn that ran it.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-copilot-resumed-turn`, then
  `dash0 spans query` filtered to `gen_ai.conversation.id`, reading `gen_ai.usage.*` off each `chat`
  span with its start time to order them, and each `execute_tool` span's id and trace.
- Channel two, the OTel file: `qa/tools/qa-otel.py qa/runs/spec-copilot-resumed-turn`, whose
  `Per turn` block lists each turn's figures separately once there is more than one.
- The hook record: `record/index.jsonl`, counting `agentStop` rows.
- `plugin-debug.log` is where a double-emit is visible before the wire: the same tool span id
  appearing twice under different trace ids is the defect's exact signature.

## Then

- Dash0 holds exactly 2 `chat` spans and 2 `execute_tool` spans for the session.
- The earlier `chat` span's `gen_ai.usage.*` equal turn 1's `invoke_agent` figures.
- The later `chat` span's equal turn 2's, **not** the session's. This is the assertion the spec
  exists for: `29684`, not `59110`.
- No `chat` span's input tokens equal the session total.
- The two `execute_tool` spans carry **different** span ids. Two spans sharing one id is the
  re-emission signature.
- Each `execute_tool` span shares a trace with the `chat` span of the turn that ran it, and its
  parent is that span.
- Both turns' spans carry the same `gen_ai.conversation.id`, which is the id the driver pinned with
  `--session-id`.
- `qa-compare.py` exits `0`.

## Tolerance

**Token counts vary between runs.** The table is the measured shape, not a constant. Only the
agreement between each turn's `invoke_agent` figures and its span is asserted, and only the
inequality against the session total is a fixed claim.

**Turn 2's input tokens are close to turn 1's and that is not a bug.** Copilot resends the
conversation, so a two-turn session's per-turn inputs are similar numbers, and "roughly double" is
what the failure looks like rather than "roughly equal". Compare against the file, never against a
mental model of what a turn should cost.

**`cache_read` differing between the turns is a property of this workload**, not an invariant.

**The failure this spec was written from.** Measured 2026-08-28 on `qa/runs/probe-two-turns`: turn
2's `chat` span carried 59068 input tokens for a turn of 29655, and the second `execute_tool` span
list held turn 1's span id `2c3471d66cc846cd` a second time under turn 2's trace. The cursor lived
in the per-session directory that `pipeline.Process` removes on `SessionEnd`, and a Copilot session
id outlives its session. It now lives beside the native-OTel files, keyed by conversation, where the
existing stale-file sweep also cleans it up.

**If this fails again, look for the cursor first.** Re-run with `QA_KEEP_SCRATCH=1` and check that
`<scratch>/.local/state/dash0-agent-plugin/copilot/otel/cursor-<session>.json` exists after the
first turn. An absent cursor is this defect; a present one that did not help is a different bug.

**Ingest lag: 25 seconds.** This spec is where the shorter wait was measured as misleading — at 8
seconds Dash0 held turn 1's two spans and neither of turn 2's, which reads exactly like the plugin
stopping halfway through.
