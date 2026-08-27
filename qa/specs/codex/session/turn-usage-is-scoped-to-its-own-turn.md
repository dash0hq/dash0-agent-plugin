---
id: turn-usage-is-scoped-to-its-own-turn
area: codex/session
runtime: codex
status: active
input: qa/tools/qa-session-codex.sh with QA_CODEX_RESUME, two turns in one session
duration: ~25s
settling: 8s
cleanup: keep
covers:
  - internal/source/codex/rollout.go
  - internal/source/codex/codex.go
  - internal/pipeline/pipeline.go
---

## Given

The plugin provisioned into a throwaway home by `qa-session-codex.sh`, exporting to the target in
`qa/config.local.json`, plus the QA recorder. Nothing on the machine is configured or mutated; see
`## Runtimes` in [../../setup.md](../../../setup.md).

**Two turns, one session.** One `codex exec` is one turn, so a single-turn run cannot distinguish
"this turn's usage" from "the session's usage": with one turn the two are the same number. The
second turn is what makes the assertion possible at all, and `QA_CODEX_RESUME` drives it through
`codex exec resume --last`, which keeps the session id and fires a second `SessionStart`,
`UserPromptSubmit` and `Stop`.

Both turns must call a tool. A turn with no tool call makes one model round-trip, and a per-turn
total that happens to equal a single call cannot show a summing error.

## When

```sh
QA_CODEX_REUSE_LOGIN=1 \
QA_CODEX_RESUME='Now run the shell command: echo turn-two. Then reply with exactly the word two.' \
  qa/tools/qa-session-codex.sh \
  'Run the shell command: echo turn-one. Then reply with exactly the word one.' \
  spec-codex-turn-usage
sleep 8
```

Shape, measured on plugin 0.1.25 with codex-cli 0.149.1: 10 hook invocations, 2 `Stop`, 2 `chat`
spans, 2 `execute_tool Bash` spans, 4 `token_count` events in one rollout, and `"turns": 2` in the
manifest.

## Expectation

From `qa/runs/spec-codex-turn-usage/record/` and `rollout.jsonl` alone.

**Partition the rollout's `token_count` events by the recorder's `Stop` timestamps.** Each
`token_count` payload carries `info.last_token_usage` for one model round-trip. Each `Stop` row in
`record/index.jsonl` carries the wall-clock instant a turn ended. A `token_count` whose timestamp is
at or before the first `Stop` belongs to turn 1; the rest belong to turn 2. Summing each group gives
that turn's input, output and cache-read totals.

**Why the partition comes from the hooks and not from the rollout's own turn markers.** The product
decides where a turn starts by reading the rollout, and it reads it wrongly once already: it keyed on
`user_message`, which codex-cli 0.149.1 never writes, so every turn after the first reported the
whole session. An expectation built on the same marker the product uses would have agreed with that
bug. The recorder's `Stop` times are written by a different process that reads no rollout at all,
which is what makes this an independent expectation rather than a restatement.

Measured on the reference run:

| | input | output | cache_read |
| --- | --- | --- | --- |
| turn 1 | 29184 | 113 | 24064 |
| turn 2 | 29491 | 94 | 24064 |
| session | 58675 | 207 | 48128 |

The session row is there to be *contradicted*: before the fix, turn 2's span carried it.

## Oracle

- Channel one, Dash0: `qa/tools/qa-compare.py qa/runs/spec-codex-turn-usage`, then
  `dash0 spans query` filtered to `gen_ai.conversation.id`, reading
  `gen_ai.usage.*` off each `chat` span with its `startTimeUnixNano` to order them.
- Channel two, the rollout: `qa/tools/qa-rollout.py qa/runs/spec-codex-turn-usage/rollout.jsonl`
  gives the session totals and the `token_count` count. **Its `turn` column is not the per-turn
  expectation here** — it counts `user_message` boundaries, of which a 0.149.1 rollout has none, so
  its `turn` equals its `file`. That is deliberate: it is the independent reader and must not learn
  the product's rule. Compute the partition as `## Expectation` says.

## Then

- Dash0 holds exactly 2 `chat` spans for the session, one per `Stop`.
- The earlier `chat` span's `gen_ai.usage.input_tokens`, `output_tokens` and
  `cache_read.input_tokens` equal turn 1's partition sums.
- The later `chat` span's equal turn 2's partition sums, **not** the session sums. This is the
  assertion the spec exists for: `29491`, not `58675`.
- No `chat` span's input tokens equal the session total.
- The two `chat` spans carry the same `gen_ai.conversation.id` and different span ids.
- `qa-compare.py` exits `0`.

## Tolerance

**A `token_count` event landing within milliseconds of a `Stop` is ambiguous.** The partition uses
`timestamp <= stop`, and the recorder's clock and Codex's rollout clock are two reads of the same
system clock from different processes. If a boundary event ever straddles the two, re-derive that
one event's turn from its position in the file — `task_started` and `task_complete` bracket each
turn — rather than reporting a difference. This has not been observed: on the reference run the
nearest event was 1.9s clear of its `Stop`.

**Token counts vary between runs.** The table is the measured shape, not a constant. Only the
agreement between the partition and the spans is asserted, and only the inequality against the
session total is a fixed claim.

**Which model Codex picks is not fixed.** The reference run used `gpt-5.6-terra`, whichever the
account defaults to. A different model changes every number and no assertion here.

**`cache_read` being equal across both turns is a coincidence of this workload**, not an invariant.
Do not assert it.

**Ingest lag.** A few seconds, as everywhere in this suite. Zero spans immediately after the session
is lag until a re-run says otherwise.

**A resumed turn needs the scratch home the first turn built.** `QA_CODEX_RESUME` keeps it alive
across both turns and deletes it afterwards. Running the second turn by hand needs
`QA_KEEP_SCRATCH=1` and the exec flags **before** the `resume` subcommand; Codex rejects them after
it.
