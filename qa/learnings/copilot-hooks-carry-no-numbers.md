# A Copilot run's second channel is also the plugin's input, so agreement proves less

Claude Code's transcript and Codex's rollout are records the plugin reads *as well*, but a
count taken from them owes nothing to the plugin's own arithmetic: the hook payloads carry
the usage, and the second channel is a separate description of the same session.

Copilot has neither half of that. Its hook payloads carry **no tokens, no cost, no model
and no tool events the plugin consumes** — `agentStop` carries `stopReason` and
`stop_hook_active` and nothing else. Everything quantitative is read from Copilot's own
OpenTelemetry file at each turn boundary, and that file is what `qa/tools/qa-otel.py`
reads back as the second channel.

So a green Copilot token comparison means the plugin copied its input faithfully. It does
not mean Copilot measured the session correctly, and no report may say that it does.

**Why it matters:** the report looks identical to a Claude or Codex one, three columns
agreeing to the token, and it is tempting to quote it with the same confidence. Two of
those columns have one source.

**How to apply:** on a Copilot run, lean on the claims the shared input cannot launder.
The hook record independently gives one `chat` span per `agentStop`. The span *tree* —
which span parents which — is structural and no token count can hide a break in it.
`qa-otel.py` also prints Copilot's own per-turn roll-up next to the sum of that turn's
chat spans, which catches Copilot disagreeing with itself. Prefer those three over a
token equality when writing an assertion. See
[[copilot-otel-rollup-differs-on-a-delegating-turn]].
