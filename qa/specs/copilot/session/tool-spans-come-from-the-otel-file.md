---
id: tool-spans-come-from-the-otel-file
area: copilot/session
runtime: copilot
status: active
input: qa/tools/qa-session-copilot.sh, one turn with one tool call
duration: ~20s
settling: 25s
cleanup: keep
covers:
  - internal/source/copilot/otelfile.go
  - internal/source/copilot/copilot.go
  - cmd/copilot-on-event/main.go
---

## Given

The plugin installed into a throwaway home by `qa-session-copilot.sh`, exporting to the target in
`qa/config.local.json`, plus the QA recorder. The recorder registers `postToolUse` and
`postToolUseFailure` **even though the plugin ignores them**, which is what makes this spec possible
at all: it gives QA the hook the plugin refused, so the two can be told apart.

**The plugin deliberately does not build tool spans from tool hooks.** `internal/source/copilot`
drops `postToolUse` and `postToolUseFailure` for two reasons: the payload carries no duration, so a
span built from it is a zero-length instant, and the hook never fires inside a sub-agent, so a
delegating turn would lose every tool the sub-agent ran. Tool spans come from the native-OTel file's
`execute_tool` spans instead, with their real start and end times and their native span ids reused
verbatim.

That makes this spec structural rather than numeric, and deliberately so. A count comparison here
would compare the OTel file against spans built from the OTel file. **A duration cannot be
laundered that way**: a hook-built span would be an instant, and an instant is distinguishable from
a real measurement no matter which side of the comparison you stand on.

## When

```sh
QA_COPILOT_BINARY=working-tree qa/tools/qa-session-copilot.sh \
  'Run the shell command: sleep 2 && echo qa-probe. Then reply with exactly the word done.' \
  spec-copilot-tool-spans
sleep 25
```

The `sleep 2` is the point. A tool that finishes instantly cannot distinguish a real duration from a
zero-length one, so the workload has to take measurable time.

Shape, measured on plugin 0.1.25 with Copilot CLI 1.0.80: 5 hook invocations of which 1 is
`postToolUse`, one native-OTel file with 1 `execute_tool bash` span, and 2 spans in Dash0. The
emitted tool span ran **2050 ms** for a 2-second sleep, carried span id `a00f07b114536a8b` — the
file's own — and `dash0.gen_ai.tool.bash.command_family` of `sleep`. The turn's `chat` span ran
6217 ms, which brackets it.

## Expectation

**From the native-OTel file, read by `qa/tools/qa-otel.py`**, which re-reads the format rather than
importing `internal/source/copilot`:

- one `execute_tool` span per tool the turn ran, named by `gen_ai.tool.name`;
- each with a `startTime` and `endTime` at least as far apart as the work took;
- each with a native `spanId`, which the plugin reuses verbatim rather than minting a new one.

**From the hook record, independently**: one `postToolUse` row per tool the *parent* turn ran, with
`toolName` naming it. On a turn with no sub-agent the two lists have the same members, and that
agreement is the only part of this spec the shared input cannot supply — the recorder wrote its row
without reading the file.

**The span id is the assertion that separates the two sources.** A tool span built from a hook would
carry a freshly minted id. One built from the file carries the file's, which is a 16-hex value the
plugin never generated.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-copilot-tool-spans` for the counts, then
  `dash0 spans query` filtered to `gen_ai.conversation.id` reading each `execute_tool` span's id,
  parent, start and end.
- Channel two, the OTel file: `qa/tools/qa-otel.py qa/runs/spec-copilot-tool-spans`.
- The hook record: `record/index.jsonl` and `record/events/*-postToolUse.json`.

## Then

- Dash0 holds one `execute_tool` span per `execute_tool` span in the file, with the same
  `gen_ai.tool.name`.
- Each `execute_tool` span's id equals the native span id in the file. It was **not** minted by the
  plugin.
- Each `execute_tool` span has a **non-zero duration**, and the one wrapping `sleep 2` is at least
  2 seconds. This is the assertion the spec exists for: a hook-built span would be an instant.
- Each top-level `execute_tool` span's parent is the turn's `chat` span.
- `gen_ai.tool.call.id` is Copilot's own `call_…` identifier from the file.
- `gen_ai.tool.call.arguments` is the decoded argument object, not the raw JSON string the file
  carries — the plugin decodes it so the shared extractors see the same shape the other runtimes'
  hooks deliver.
- `dash0.gen_ai.tool.bash.command_family` is `sleep`, derived by the same extractor the other
  runtimes use.
- `qa-compare.py` exits `0`, and its tool table prints the `postToolUse` counts as a second opinion
  rather than as the expectation.

## Tolerance

**The recorded `postToolUse` count is not an expectation, and `qa-compare.py` says so where it
prints it.** On a delegating turn Dash0 legitimately holds more tool spans than hooks fired, because
sub-agent tools fire no hook at all. Only on a turn with no sub-agent do the two lists match, which
is why this spec's workload has none.

**Duration is a floor, not an equality.** The span covers Copilot's own view of the tool execution,
which includes its overhead around the command. Assert `>= 2s`, never `== 2s`.

**A non-zero shell exit is not a failed tool.** Measured 2026-08-28: `exit 3` produced
`"resultType": "success"` in the hook payload and no error status on the native span. Nothing here
asserts a failure status, and a spec that wants one needs a tool that fails at the tool level.

**A late flush folds into the next turn.** A native span Copilot writes after the `agentStop` read
lands in the next turn's window and is emitted there, parented to that turn's `chat` span. It is
documented in `copilot/README.md`, it is rare because tools normally flush before the turn's final
round-trip, and on a one-turn run it would show up as a missing tool span rather than a misplaced
one. Re-run before filing.

**Ingest lag: 25 seconds.** See `## Settling` in [../../../setup.md](../../../setup.md).
