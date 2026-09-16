---
id: turn-produces-one-chat-root-and-one-span-per-tool
area: amp/session
runtime: amp
status: active
input: qa/tools/qa-session-amp.sh, one local turn with one tool call
duration: ~20s
settling: 10s
cleanup: keep
covers:
  - amp/index.ts
  - internal/source/amp/amp.go
  - internal/otlp/trace.go
---

## Given

The dash0 plugin **as this machine has it installed**, at
`~/.config/amp/plugins/dash0/`, configured for this run through
`AMP_PLUGIN_OPTION_*` so it exports to `ampDataset` in `qa/config.local.json`.
The QA recorder is installed project-scoped alongside it. See `## Runtimes` in
[../../setup.md](../../../setup.md).

`amp-installed-plugin-matches-the-tree` must pass first. It is the only thing
standing between this spec and a run against a stale build, because the
installed plugin is a hand-built directory copy that no `git checkout` updates.

## When

```sh
qa/tools/qa-session-amp.sh \
  'Run the shell command: echo qa-amp-first. Then reply with exactly the word done.' \
  spec-amp-session
sleep 10
qa/tools/qa-amp-compare.py qa/runs/spec-amp-session
```

## Then

`qa-amp-compare.py` exits `0` and prints `AGREEMENT`, meaning all three hold:

- **one `chat` span per `amp -x`.** The turn root is a `chat` span, not
  `invoke_agent`. Dash0 reads a session's turns from `chat` spans and treats
  `invoke_agent` as a sub-agent invocation, so an `invoke_agent` root would
  detach each model call from the turn that made it.
- **one `execute_tool` span per `tool_use` block** in Amp's own
  `--stream-json`, which the plugin never sees.
- **the tool names agree** between the two channels.

The spans carry `gen_ai.conversation.id` equal to the thread id, which is what
makes a filtered read possible at all. An unfiltered count against the
whole dataset is meaningless.

## Not asserted here

**Tokens.** Usage export is off in this spec, so the root carries no model and
no token attributes, and that is correct rather than a failure. Turning it on
does not fix it either — see
[amp-answering-model-call-is-never-attributed](../../../findings/amp-answering-model-call-is-never-attributed.md).

**That the tool actually ran.** The spec asserts the span shape, not the shell
effect. A turn where the model declines to call the tool is a failed *run* of
this spec, not a failing assertion; re-run it rather than asserting over one
span.
