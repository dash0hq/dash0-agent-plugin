# A Cursor transcript corroborates turns and nothing else

The `cursor` runtime's second channel is Cursor's own agent transcript, under
`~/.cursor/projects/<slug>/agent-transcripts/<id>/`. The plugin never reads it, so it is
genuinely independent — and it is independent about less than it looks.

**No token count appears in it.** None. Measured 2026-09-01 across five runs: a transcript
entry is a message with typed content blocks, plus a `turn_ended` marker, and nothing carries
a number. Cursor exposes usage in exactly one place, the `afterAgentResponse` hook payload,
which is also the plugin's input. **So the cursor runtime has no independent reading of a token
count at all** — weaker than Claude, where the transcript is a real second measurement, and
weaker than Copilot, whose OTel file at least comes from the host's own instrumentation.

**Its tool count is a superset in another vocabulary.** Measured on
`qa/runs/probe-cursor-mcp`: 15 `tool_use` blocks in the transcript against 11 `postToolUse`
hooks and 11 spans in Dash0, with every one of the four accounted for.

| transcript | hooks |
| --- | --- |
| `Glob` 3 + `Grep` 3 | `Grep` 6 |
| `GetDynamicTools` 4 + `CallDynamicTool` 1 | `MCP:echo_text` 1 |
| `Read` 3, `Shell` 1 | `Read` 3, `Shell` 1 |

So Cursor collapses two tools into one hook name, and it records internal plumbing that fires
no hook at all.

**Why it matters:** comparing the transcript's tool count reported four phantom missing spans
on a healthy run, and a token column filled from it would have been zeros reading as a
disagreement with Dash0.

**How to apply:** `qa-transcript-cursor.py` reports `no_usage: true` and `qa-compare.py` prints
`-` in the token column with the reason. The transcript's tool table is printed as a second
opinion and never compared. Turns *are* compared, from the `<user_query>` entries — see
[cursor-turn-ended-is-per-loop-not-per-turn](cursor-turn-ended-is-per-loop-not-per-turn.md).

The tool table is still worth printing. It is the only channel that saw the sub-agent's tool
call in [../findings/cursor-subagent-work-produces-no-span.md](../findings/cursor-subagent-work-produces-no-span.md),
because the transcript logs a sub-agent's work inline while the spans have none of it.
