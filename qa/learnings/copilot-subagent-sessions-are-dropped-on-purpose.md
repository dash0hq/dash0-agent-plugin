# A Copilot sub-agent gets a partial hook lifecycle, and that is how it is recognised

A Copilot sub-agent spawned through the `task` tool gets a hook lifecycle of its own,
under its own session id, and nothing in those payloads links back to the parent
conversation.

**It is a partial lifecycle, and that is what the product keys on.** Measured
2026-08-28 across both modes: a sub-agent receives `userPromptSubmitted`, any
`postToolUse`, and `agentStop` — and **no `sessionStart`, no `sessionEnd`**. Only the
session a person started gets those. The id alone is not usable: `copilot -p` names a
sub-agent session `call_<toolCallId>`, an interactive session gives it a plain UUID.

`internal/source/copilot` drops every `call_`-prefixed session wholesale. Processing them
would mint a standalone, token-less "conversation" per sub-agent: spans attached to
nothing, with no usage, that no consumer can join to the session they came from.

The QA recorder still records them, because it records what the host offered rather than
what the plugin accepted. So a delegating run's `record/index.jsonl` holds rows for two or
more session ids, and only one of them is the run's.

**Why it matters:** two things rest on this. `qa-compare.py` used to report those rows as
"invocations from an earlier session in this directory — the run id was reused", which
sends the reader after a harness problem that does not exist; it now counts them
separately and names them. And the product suppresses a sub-agent by the missing
`sessionStart`, so an earlier version of this file — which claimed a sub-agent runs its
own `sessionStart` and `sessionEnd` — documented the opposite of the invariant the fix
depends on.

**How to apply:** on a delegating Copilot run, expect more session ids in the recording
than in Dash0, and expect Dash0 to hold exactly one conversation for the turn. Do not
check for the absence of a `call_` prefix: that is the prompt-mode id, and an interactive
sub-agent's is a plain UUID indistinguishable from a real session's. Compare the set of
conversation ids instead. The sub-agent's work arrives through the native-OTel file instead, as its own
`invoke_agent` span under the `task` tool that spawned it, with that agent's tools beneath
it. Under `copilot -p` the `call_` id is not wasted: it becomes that span's
`gen_ai.agent.id`, so the dropped hook session and the emitted span name the same
sub-agent. Interactively the hook session is a plain UUID while `gen_ai.agent.id` is
still the spawning tool call's id, so that join holds in prompt mode only.
