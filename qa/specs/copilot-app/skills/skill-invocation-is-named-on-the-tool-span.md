---
id: skill-invocation-is-named-on-the-tool-span
area: copilot-app/skills
runtime: copilot-app
status: draft
input: the qa-echo fixture (qa/skill-fixture/qa-echo) in the target worktree's .github/skills/
duration: ~15s
settling: 25s
cleanup: keep
covers:
  - internal/source/copilotapp/copilotapp.go
  - internal/source/copilot/emit.go
  - internal/pipeline
---

## Given

The runner procedure in [../README.md](../README.md), with a real model. After `prepare` and before the first
message, copy the fixture into the target's worktree:

```sh
mkdir -p <session-worktree>/.github/skills && cp -R qa/skill-fixture/qa-echo <session-worktree>/.github/skills/
```

The app loads a repository's skills from `.github/skills/`. Measured 2026-10-06 on
`qa/runs/copilot-app-skill-20261006`, where the model called `qa-echo` from there and ran its
`echo`.

Do not use the extension's own `dash0-configure` skill: following it writes the config file.

## When

Send:

```text
Use the qa-echo skill.
```

Wait for the session to go idle, then wait out the settling time.

## Expectation

**From `events.jsonl`, independently:** a main-agent `tool.execution_start` whose `toolName` is
`skill` and whose `arguments` name `qa-echo`. Also, after it, a `bash` call whose command is
`echo QA-SKILL-MARKER`. That proves the skill's body reached the model, not just its name.

## Oracle

- Dash0: `dash0-spans.json`.
- The event log: `events.jsonl` in the run directory.

## Then

- The `skill` call is exactly one `execute_tool skill` span, matched on `gen_ai.tool.call.id` =
  `toolCallId`.
- It carries `dash0.gen_ai.tool.skill.name` equal to the skill named in the call's arguments, and
  `dash0.gen_ai.tool.skill.source` = `model`.
- The `chat` span carries neither key. On Copilot, `DEVELOPMENT.md` puts the skill only on the tool
  span.
- The `echo QA-SKILL-MARKER` call is its own `execute_tool bash` span in the same trace.

## Tolerance

**The app may load skills without a `skill` tool call**, for example by inlining the instructions.
Then the event log has the `echo` call and no `skill` call. The spec cannot be judged. Report it as
a finding about the app's shape, not as a plugin failure.

**The qualified name belongs to the app.** If the arguments say `qa-echo` under a prefix, expect
that exact string.

**Ingest lag: 25 seconds, and up to five minutes in this runtime.** The debug log is complete at once. When Dash0 holds fewer spans than it, re-query for up to five minutes before calling one missing.
