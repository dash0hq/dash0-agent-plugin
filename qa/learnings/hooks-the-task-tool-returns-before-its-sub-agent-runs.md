# The Task tool returns in milliseconds, so a sub-agent outlives the turn that spawned it

The `Agent` tool's `PostToolUse` reports a `duration_ms` of 2 or 3, on four measured runs. The call
launches the sub-agent and returns; it does not wait. The spawning turn's `Stop` then fires 2.2 to
3.1 seconds later, and the sub-agent keeps working. When it finishes, its result arrives as a
`<task-notification>` injected as a fresh `UserPromptSubmit`, which opens a second turn and a second
`chat` span.

So the hook sequence for one delegating prompt is: `PreToolUse(Agent)`, `SubagentStart`,
`PostToolUse(Agent)`, then the sub-agent's own `PreToolUse`/`PostToolUse` pairs interleaved with the
main session's `Stop`, then `SubagentStop`, `UserPromptSubmit`, `Stop`.

**Why it matters:** two things that look like bugs are not, and one that looks fine is not.

- Two `chat` spans for one user prompt is correct. The delegation costs a turn.
- `Stop` arriving while the sub-agent is mid-tool-call is correct, not a lost turn.
- A sub-agent probe that makes one fast tool call is the least representative case there is. It
  finishes inside the two-and-a-half-second window before `Stop`, so it exercises none of the
  after-`Stop` behaviour. Two such runs looked like proof the sub-agent path worked; a sub-agent that
  worked for ten seconds showed the opposite.

**How to apply:** make the sub-agent do at least three sequential tool calls, so its later ones are
guaranteed to land after the spawning turn's `Stop`. Then read the ordering out of
`record/index.jsonl` before reading any count, because that is the only place it is visible after the
fact:

```sh
python3 -c "
import json
rows=sorted((json.loads(l) for l in open('qa/runs/<id>/record/index.jsonl')), key=lambda r: r['seq'])
t0=rows[0]['seq']
for r in rows: print(f\"+{(r['seq']-t0)/1e9:6.2f}s {r['hook_event_name']}\")
"
```

Related: [[hooks-transcript-does-not-exist-at-session-start]], another place where a hook's timing
rather than its content decides what the pipeline sees.
