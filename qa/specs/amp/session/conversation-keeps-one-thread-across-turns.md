---
id: conversation-keeps-one-thread-across-turns
area: amp/session
runtime: amp
status: active
input: qa/tools/qa-session-amp.sh with QA_AMP_TURNS, five turns on one thread
duration: ~3m
settling: 30s
cleanup: keep
covers:
  - amp/index.ts
  - internal/source/amp/amp.go
  - internal/otlp/trace.go
---

## Given

The installed plugin, configured through `AMP_PLUGIN_OPTION_*` to export to
`ampDataset`, with the QA recorder alongside it. `amp-installed-plugin-matches-
the-tree` must pass first.

A single exchange proves almost nothing about Amp, because every one of the
bridge's per-turn mechanics is a *second* turn problem: the thread state map is
keyed by thread id and deleted at `agent.end`, the turn id must change so that
spans do not collide, and `usage:<turn>:<n>` span ids were once
`usage:<n>` and reused one id in every trace. This spec is the one that runs
long enough for those to be wrong.

## When

`QA_AMP_TURNS` is a file of one prompt per line, each run through
`amp threads continue` on the thread the first `-x` opened. It is the only way
to get a real multi-turn thread: piping several lines into one `amp` feeds them
as one prompt, and `-x` archives the thread on exit without
`--no-archive-after-execute`.

```sh
cat > /tmp/qa-amp-turns.txt << 'EOF'
Run the shell command: echo qa-turn-two. Then reply with exactly the word done.
Run the shell command: qa-this-command-does-not-exist. Do not try an alternative and do not fix it. Then reply with exactly the word done.
Run the shell command: echo qa-turn-four. Then reply with exactly the word done.
Without running any command, reply with exactly the codeword I gave you in my first message, and nothing else.
EOF

QA_AMP_USAGE=1 QA_AMP_TURNS=/tmp/qa-amp-turns.txt \
  qa/tools/qa-session-amp.sh \
  'Remember the codeword: ZEPHYR-41. Run the shell command: echo qa-turn-one. Then reply with exactly the word done.' \
  spec-amp-conversation
# 30s, not 10s: with usage export on the helper is detached and polls
# `amp threads export` for up to twenty seconds, so spans land after `amp` exits.
sleep 30
qa/tools/qa-amp-compare.py qa/runs/spec-amp-conversation
qa/tools/qa-attrs.py qa/runs/spec-amp-conversation --dataset <ampDataset>
```

## Then

`qa-amp-compare.py` exits `0` with `AGREEMENT`, and:

- **one thread.** Every `system` event in `stream.jsonl` carries the same
  `session_id`, and every span matches the single
  `gen_ai.conversation.id` filter. Five `chat` roots, not five threads.
- **five `chat` roots, four `execute_tool` children.** Turn five calls no tool
  by design, so a fifth tool span would mean a span leaked across turns.
- **five distinct `dash0.amp.turn.id` values**, one per root, and each
  `execute_tool` carries the same value as its own root. A repeated turn id is
  the state-map-keyed-by-thread bug; a tool span carrying the previous turn's
  id is the span-id collision one.
- **the conversation is real.** Turn five's answer is `ZEPHYR-41`, the codeword
  given only in turn one. Without this, five `amp threads continue` calls that
  silently started five fresh threads would still satisfy every span assertion
  above.
- `qa-attrs.py` prints `Every attribute is in the documented contract.`

## Not asserted here

**Token totals.** Usage export is on, and it is normal for most roots to report
`dash0.amp.usage.status=partial` with no model and no tokens — see
[amp-answering-model-call-is-never-attributed](../../../findings/amp-answering-model-call-is-never-attributed.md).
The measured run had one turn of five reach `matched`, which is a race, not a
number to assert. `qa-amp-compare.py` treats the turn row as a lower bound when
usage is on, because a `matched` turn adds a `chat` child.

**That turn three's tool span is an error.** It is not, and that is correct —
see [tool-status-is-copied-from-amp-not-inferred](tool-status-is-copied-from-amp-not-inferred.md).

**A tool the model declines to call** is a failed run, not a failing assertion.
Re-run it.
