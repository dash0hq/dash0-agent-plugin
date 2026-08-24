# A session's cost is reproducible to the microdollar from the transcript and a price table

No span carries a cost. Dash0 derives `dash0.gen_ai.usage.cost` at ingest from the token attributes
the plugin sent, so the figure can be recomputed without either of them: take the `usage` blocks in
Claude Code's transcript and multiply by Anthropic's published list prices.

Verified against every `chat` span in a 24-hour window, 82 of them across `claude-haiku-4-5` and
`claude-opus-5`. Zero mismatches at a slack of one microdollar. Per million tokens:

| Model | Input | Output | Cache read |
| --- | --- | --- | --- |
| `claude-haiku-4-5` | $1.00 | $5.00 | $0.10 |
| `claude-opus-5` | $5.00 | $25.00 | $0.50 |

A cache write costs 1.25x the input rate for a 5-minute lifetime and 2x for an hour. **The two
lifetimes are priced apart, not blended.** One session wrote 18,299 tokens at 5 minutes and 19,840
at an hour, and the cost matched the split exactly; a single blended rate would have been 19% low or
18% high. The breakdown reaches Dash0 as
`dash0.gen_ai.usage.cache_creation.ephemeral_{5m,1h}.input_tokens`, and
`internal/pipeline/pipeline.go` sets it only from the Claude transcript. No other source emits it,
so this result does not transfer to the other agents.

`claude-result.json`'s `total_cost_usd` is a third figure for the same value, and it agreed to the
microdollar in every run. Unlike its `usage` block, it does include sub-agent cost — see
[[usage-claude-result-json-omits-subagent-usage]].

**Why it matters:** cost is the number a user checks first, and it is the one number no channel
can be compared against without a price table. Without one, a cost finding is unfalsifiable.

**How to apply:** run `qa/tools/qa-cost.py qa/runs/<id>`. It holds the table, brackets the
cache-write lifetimes, and refuses to guess for a model it has no rates for rather than reporting an
expectation that is silently too low. Add a new model's rates there, and mark them unverified until
a real span reproduces them.
