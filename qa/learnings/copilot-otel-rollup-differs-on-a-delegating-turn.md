# Copilot's own per-turn roll-up excludes sub-agent usage, and the plugin's chat span includes it

Copilot writes one top-level `invoke_agent` span per agent turn, carrying that turn's
usage already summed. On a plain turn it equals the sum of the turn's `chat` spans, which
is what the plugin puts on its own `chat` span, and the two agree to the token.

On a turn that delegates they do not. The roll-up counts only the parent agent's
round-trips; the sub-agent's `chat` spans share the parent's `gen_ai.conversation.id` and
the plugin sums those in too — flat attribution, documented in `copilot/README.md`.
Measured 2026-08-28 on `qa/runs/probe-subagent`: 29563 input and 232 output from the
roll-up, against 52038 and 298 from the chat spans, with Dash0 correctly carrying the
latter.

**Why it matters:** it is a large, clean-looking divergence between two figures in the
same file, and reading it as the plugin over-counting is the obvious mistake. It is
Copilot's arithmetic, and the plugin's number is the one that describes what the turn
actually cost.

**How to apply:** `qa/tools/qa-compare.py` prints the gap with this explanation whenever
it appears, and `qa/tools/qa-otel.py` shows both columns. Before treating a difference as
a finding, check whether the run delegated — `subagent_tools` in the `qa-otel.py` output
is non-zero when it did. See [[copilot-hooks-carry-no-numbers]].
