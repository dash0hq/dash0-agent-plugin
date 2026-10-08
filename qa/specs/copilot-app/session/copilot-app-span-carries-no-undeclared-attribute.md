---
id: copilot-app-span-carries-no-undeclared-attribute
area: copilot-app/session
runtime: copilot-app
status: draft
input: the run directories of the other copilot-app specs — no session of its own
duration: instant
settling: 25s
cleanup: keep
covers:
  - internal/source/copilotapp/copilotapp.go
  - internal/source/copilot/emit.go
  - cmd/copilot-app-on-event/main.go
  - DEVELOPMENT.md
---

## Given

The run directories of the other `copilot-app` specs, each collected with `qa-app-run.py collect`.
The more kinds of span they reach, the more this spec covers. The ones that matter most are:
- a sub-agent: [first-turn-is-traced-with-its-subagent](../subagents/first-turn-is-traced-with-its-subagent.md);
- a failure: [failed-first-turn-is-reported](../turns/failed-first-turn-is-reported.md);
- an MCP call: [mcp-call-names-the-server](../mcp/mcp-call-names-the-server.md);
- a skill: [skill-invocation-is-named-on-the-tool-span](../skills/skill-invocation-is-named-on-the-tool-span.md).

The app forwards a payload whose keys the plugin does not choose: the extension trims each event to
a list, but the binary copies whatever reaches it. Cursor shipped five raw keys this way, among them
a JSON array of absolute paths. The contract in `DEVELOPMENT.md` is maintained by hand and the
pipeline never reads it, so a key outside it is a defect either in the export or in the document.

## When

For each run directory:

```sh
qa/tools/qa-attrs.py qa/runs/<run-id>
```

## Expectation

**From `DEVELOPMENT.md`, independently:** the attribute tables under `Resource attributes`,
`On every span`, `LLM / chat spans` and `Tool-call spans`. `qa-attrs.py` parses them, and nothing in
the plugin produces them.

## Oracle

- Dash0, through `qa-attrs.py`, which queries the session's spans and compares every observed key
  with the contract. Keys added at ingest are listed separately and are not judged.

## Then

- `qa-attrs.py` exits `0` for every run directory.
- In the union of all runs, `dash0.gen_ai.tool.mcp_server` and `dash0.gen_ai.tool.skill.name` were
  observed, if the specs that reach them ran. Otherwise, say which keys this pass never saw.

## Tolerance

**Exit `2` is not a verdict.** It means a query failed or the result was truncated. Re-run it.

**Keys added at ingest** (`dash0.*` resource and operation keys, `user.id`) are informational, and
`qa-attrs.py` already sets them apart.

**Ingest lag: 25 seconds, and up to five minutes in this runtime.** The debug log is complete at once. When Dash0 holds fewer spans than it, re-query for up to five minutes before calling one missing.
