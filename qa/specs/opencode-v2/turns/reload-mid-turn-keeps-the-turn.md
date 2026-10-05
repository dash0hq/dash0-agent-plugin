---
id: reload-mid-turn-keeps-the-turn
area: opencode-v2/turns
runtime: opencode-v2
status: active
input: qa/tools/qa-session-opencode-v2.sh with QA_OPENCODE_V2_RELOAD_ON='sleep 10' and QA_OPENCODE_V2_RELOAD_AFTER=2, one turn whose shell call sleeps 10s
duration: ~45s
settling: 25s
cleanup: keep
covers:
  - opencode-v2/index.js
  - internal/source/opencodev2/opencodev2.go
---

## Given

The driver with `QA_OPENCODE_V2_RELOAD_ON='sleep 10'` and `QA_OPENCODE_V2_RELOAD_AFTER=2`: two
seconds after OpenCode logs spawning the shell call's `sleep 10`, it runs `opencode reload --server`
against the private server. A reload stops
the plugin's consumer and starts a new one, which reloads the correlation state the old one saved.

**Mid-tool, because that is where a reload can hurt.** The tool span was opened by the old
consumer and is closed by the new one, and the turn's usage spans both. The driver records the
exporter PIDs either side of the reload, so a reload that silently did nothing cannot pass.

**Waiting for the tool, not the clock.** A timer started with the turn can fire while the model is
still writing the tool call. Measured 2026-10-06: a reload 4s into the turn landed before the shell
started, OpenCode aborted that call and retried it, and `opencode run` exited 1. The driver also
records when the reload started and finished, so a reload that still misses the tool is caught.

## When

```sh
QA_OPENCODE_V2_RELOAD_ON='sleep 10' QA_OPENCODE_V2_RELOAD_AFTER=2 qa/tools/qa-session-opencode-v2.sh \
  'Run the shell command `sleep 10 && echo slept`. Then reply with exactly the word done.' \
  spec-opencode-reload
sleep 25
```

## Expectation

- The exporter PIDs after the reload share none with those before it.
- In the export, the shell call's `time.ran` is before the reload started and its `time.completed`
  after the reload finished.
- The turn still exports one `chat` span and one `execute_tool shell` span, parented on it in the
  same trace, with the export's token counts.

## Oracle

- `qa/tools/qa-compare.py qa/runs/spec-opencode-reload`, which also checks the PIDs and the export's tool timings
  against the reload's. A reload that missed the tool exits `2`: the run tested nothing.

## Then

- `qa-compare.py` exits `0`.

## Tolerance

**Events between the old subscription stopping and the new one starting can be lost.** That is the
documented limitation of the V2 stream. Losing one is a finding to record with the debug log, not
a reason to loosen the counts: during a sleeping tool call there are no events to lose.
