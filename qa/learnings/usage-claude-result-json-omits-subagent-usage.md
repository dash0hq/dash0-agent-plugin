# Claude Code's own usage figures exclude sub-agent usage

`claude -p --output-format json` reports a `usage` block and a `total_cost_usd` from
Claude Code's in-process accounting. For a session that spawned a sub-agent, the `usage`
block covers the main session only. `total_cost_usd` does not: on the session below it
matched the price table over both transcripts to the microdollar, so cost is whole and
tokens are not. One measured session:

| | input | output | cache read |
| --- | --- | --- | --- |
| Dash0 spans, and the transcripts | 48 | 884 | 97,785 |
| `claude-result.json` | 10 | 81 | 34,877 |

Both are correct about what they measure. The spans include the `invoke_agent` span and
the sub-agent's own tool calls, whose usage lives in the sub-agent's separate transcript.

**Why it matters:** the gap is large and looks exactly like the product over-reporting
tokens. It is the single most tempting false finding in this setup, and it grows with how
much work the sub-agent did.

**How to apply:** use `claude-result.json` for cost, which no span carries, and never for
token totals on a session with a sub-agent. Compare token counts against the transcripts
instead, main plus every sub-agent, which is what
`claude/tools/claude-code-usage-audit.py` sums, and which
[[cost-is-reproducible-from-the-transcript-and-a-price-table]] prices. A separate known
gap runs the other way:
Claude Code's own auxiliary model calls appear in its figures and have no span at all
(`claude/README.md`).
