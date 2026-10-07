---
id: resumed-turn-is-scoped-to-itself
area: opencode-v2/turns
runtime: opencode-v2
status: active
input: qa/tools/qa-session-opencode-v2.sh with QA_OPENCODE_V2_RESUME, two turns that each run one tool
duration: ~45s
settling: 25s
cleanup: keep
covers:
  - internal/source/opencodev2/opencodev2.go
---

## Given

The driver with a second prompt in `QA_OPENCODE_V2_RESUME`, delivered with `opencode run -s` into the
same session on the same server.

**Two turns, because one cannot show the bug.** With one turn, "this turn's usage" and "the session's
usage" are the same number. And the conversation view depends on each turn being its own `chat`
span with its own messages; on one turn a merge is invisible.

## When

```sh
QA_OPENCODE_V2_RESUME='Now count the lines of README.md with a shell command. Then reply with exactly the word done.' \
  qa/tools/qa-session-opencode-v2.sh \
  'Read README.md with the read tool. Then reply with exactly the word done.' \
  spec-opencode-two-turns
sleep 25
```

## Expectation

- Each `session.execution.started` opens a fresh turn with a fresh trace id, and each
  `session.execution.succeeded` closes it. Turn 2 is not turn 1 continued.
- Token usage is summed within a turn only. The export's assistant messages, split at each `user`
  message, give each turn's own figure.
- Each `chat` span carries its own redacted input and output message.

## Oracle

- `qa/tools/qa-compare.py qa/runs/spec-opencode-two-turns`: two per-turn token pairs, matched against
  the export's two turns.
- `dash0 spans query` filtered to the session, for trace ids and parenting.

## Then

- `qa-compare.py` exits `0`, with 2 `chat` spans and two per-turn token pairs equal to the
  export's.
- The two `chat` spans have different trace ids.
- Each tool span shares a trace with exactly one `chat` span and is parented on it: the `read` under
  turn 1, the `shell` under turn 2.
- Neither `chat` span's input tokens equal the session total from the export's `info.tokens`.

## Tolerance

**Turn 2 usually reads turn 1 from cache.** A large `cache_read` on turn 2 is the provider's
caching, and it is counted in input tokens by design. The claim is that turn 2's figure matches the
export's turn 2, not that it is small.
