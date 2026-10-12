---
id: tool-status-is-copied-from-amp-not-inferred
area: amp/session
runtime: amp
status: active
input: qa/tools/qa-session-amp.sh, one turn whose shell command exits 127
duration: ~25s
settling: 10s
cleanup: keep
covers:
  - amp/index.ts
  - internal/source/amp/amp.go
---

## Given

The installed plugin plus the QA recorder, exporting to `ampDataset`.

Claude and Cursor each have a `tool-failure-sets-the-span-status` spec. This is
the amp equivalent, and it asserts the opposite outcome, which is why it exists
as its own spec rather than as a ported copy. **Amp does not classify a nonzero
shell exit as a tool failure.** The exit code lives inside the tool-shaped
`output` payload, which is opaque to the plugin; `event.status` is `"done"`.

`internal/source/amp/amp.go` sets the span to `ERROR` when
`tool.Status != "done"`, so copying Amp's own classification is the whole
behaviour under test. Inferring failure from an exit code would mean the bridge
parsing every tool's private output shape, and getting it wrong for every tool
whose nonzero exit is expected.

## When

```sh
qa/tools/qa-session-amp.sh \
  'Run the shell command: qa-this-command-does-not-exist. Do not try an alternative and do not fix it. Then reply with exactly the word done.' \
  spec-amp-tool-status
sleep 10
```

Then read both channels. The recorder is the important one: it shows what Amp
told the plugin, which is what separates "the bridge got it wrong" from "the
bridge was told this".

```sh
python3 -c "
import json,pathlib
for l in pathlib.Path('qa/runs/spec-amp-tool-status/record.jsonl').read_text().splitlines():
    e=json.loads(l)
    if e['event']=='tool.result': print(e['payload']['status'], e['payload']['output'])
"
```

## Then

- The recorded `tool.result` carries `status: "done"` and
  `output: {"output": "...command not found\n", "exitCode": 127}`.
- The `execute_tool shell_command` span's status is **`UNSET`**, and it carries
  no `error` attribute.
- The turn root is `UNSET` too: `agent.end` reported `status: "done"`.

A span with status `ERROR` here is the failure. It would mean something started
reading exit codes out of tool payloads.

## Provoking the real error path

This spec cannot produce `status: "error"`, and no **built-in** tool can. The
ones Amp does fail visibly — `read_web_page` against an unresolvable host, for
one — are executed server-side and fire no plugin events at all, so they reach
neither the bridge nor a span. See
[amp-server-side-tools-produce-no-span](../../../findings/amp-server-side-tools-produce-no-span.md).

**An MCP tool is the exception**, and the only thing on Amp that is both
client-side and able to fail on demand. `qa/mcp-fixture`'s `always_fails`
arrives as `status: "error"` and its span is `ERROR`, so the
`tool.Status != "done"` branch in `amp.go` *is* proven on a real session — by
[../mcp/mcp-call-names-the-server-strips-the-prefix-and-reports-failure](../mcp/mcp-call-names-the-server-strips-the-prefix-and-reports-failure.md),
not here. Run that one before concluding anything about tool status.
