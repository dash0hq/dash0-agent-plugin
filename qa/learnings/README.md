# Learnings

How to operate this system during a QA run. One claim per file, filename states the claim.

A defect belongs in a run report or in a spec with `known_failure`, not here. Something a
preflight check should catch belongs in `setup.md` via `/qa-setup --repair`.

## Plugin configuration

The installed plugin's configuration is the hardest part of this setup, and both entries
below cost a full redesign of the harness before they were understood.

- [A managed plugin install's options cannot be overridden for one session](config-managed-options-cannot-be-overridden.md) — `env -u`, `--settings`, and a config file were all tried; the managed layer wins every time.
- [A project-level config file reconfigures every hook registration in the session](config-project-file-leaks-into-every-registration.md) — its `auth_token` reaches the installed plugin too, which then 401s on every export.

## Reading spans back

- [The dash0 CLI's active profile is the wrong source for a QA query](query-pass-endpoint-token-and-dataset-explicitly.md) — pass endpoint, token, and dataset explicitly or the shell decides for you.
- [A span query without --precision disabled can drop spans](query-precision-disabled-is-mandatory.md) — adaptive sampling looks exactly like missing telemetry.
- [JSON output from a span query is capped at 100 records](query-json-output-caps-at-100-records.md) — a result equal to the limit is a floor, not a total.
- [A 403 does not tell you whether the dataset exists or the token is restricted](query-403-cannot-distinguish-restriction-from-absence.md) — so the usual token-scoping probe is inconclusive here.

## Judging a run

- [The hook-to-span mapping cannot detect missing telemetry for work that fires no tool hook](oracle-the-hook-mapping-is-blind-to-work-that-fires-no-tool-hook.md) — `qa-compare.py` exits `0` when the recording is as empty as the telemetry.

## Hook and session behaviour

- [The first hooks of a session carry a transcript path that does not exist yet](hooks-transcript-does-not-exist-at-session-start.md) — absent, not broken, and the pipeline sees the same thing.
- [Per-session plugin state is deleted at SessionEnd, so it cannot be inspected afterwards](hooks-session-state-is-deleted-at-sessionend.md) — poll during the run or get a confident wrong answer.
- [The Task tool returns in milliseconds, so a sub-agent outlives the turn that spawned it](hooks-the-task-tool-returns-before-its-sub-agent-runs.md) — two `chat` spans per prompt is correct, and a one-tool-call sub-agent probe tests almost nothing.
- [Some slash commands fire no UserPromptSubmit and no Stop, so they cannot be probed](hooks-some-slash-commands-fire-no-hooks-at-all.md) — `/help` produces no turn at all, so it cannot serve as a negative control.
- [A Codex SubagentStop ends a task; a Claude one ends the agent](hooks-a-codex-subagent-is-reusable-a-claude-one-is-not.md) — the same Codex agent stops, works again and stops again, with no second `SubagentStart` to notice it by.

## What is actually under test

- [A QA session tests the last published release, not the working tree](binary-a-run-tests-the-installed-release-unless-swapped.md) — the bootstrap downloads it silently; record the binary's digest.

## Reading the numbers

- [A session's cost is reproducible to the microdollar from the transcript and a price table](cost-is-reproducible-from-the-transcript-and-a-price-table.md) — the rates, and the cache-write lifetimes that are priced apart.
- [Claude Code's own usage figures exclude sub-agent usage](usage-claude-result-json-omits-subagent-usage.md) — the most tempting false finding in this setup. Its cost figure does not.
- [Dash0 normalizes the model name at ingest and keeps the original in a second attribute](model-dash0-normalizes-the-model-name-at-ingest.md) — one span, three keys, two spellings, and it is not a defect.

## Dead ends

- [A throwaway HOME cannot run a real Claude Code session](deadend-a-throwaway-home-loses-claude-code-auth.md) — credentials do not travel with the flags.
- [Codex answers a delegation prompt directly unless multi_agent_mode is enabled](deadend-codex-does-not-delegate-without-multi-agent-mode.md) — the failure looks like a successful run, and it was written up as "this runtime has no sub-agents".
