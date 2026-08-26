# Codex answers a delegation prompt directly unless multi_agent_mode is enabled

Ask codex-cli 0.149.1 to spawn a sub-agent and it will just do the work itself, with no
`spawn_agent` call, no `SubagentStart` and no complaint. Sub-agents are behind a feature
flag: `codex exec --enable multi_agent_mode`, or `-c features.multi_agent_mode=true`.
`QA_CODEX_MULTI_AGENT=1` sets it on the driver.

**Why it matters:** the failure is a plausible-looking success. The session runs, the
spans reconcile, and the obvious conclusion is that the prompt was not persuasive enough
— or, worse, that this Codex version has no sub-agents. That second conclusion was
written into the coverage map for a day, listing the input as undrivable, when the flag
was the only thing missing. A wrong negative about what a runtime supports is expensive:
nobody re-tests a capability the notes say does not exist.

**How to apply:** before concluding a Codex capability is absent, check whether it is a
feature flag. `codex exec --help` lists `--enable`/`--disable` but not the feature names;
`strings $(command -v codex) | grep -i <capability>` finds them, which is how
`multi_agent_mode` turned up.

Then confirm the delegation actually happened rather than trusting the reply: a run that
answered directly has no `SubagentStart` in `record/index.jsonl`, and no `spawn_agent`
among the rollout's `function_call` records.

Related: [[hooks-a-codex-subagent-is-reusable-a-claude-one-is-not]], which is what the
flag lets you observe.
