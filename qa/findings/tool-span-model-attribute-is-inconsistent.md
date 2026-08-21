---
id: tool-span-model-attribute-is-inconsistent
severity: low
status: unaddressed
filed: no
found: 2026-08-21
affects: claude-on-event 0.1.24
spec: none
---

# `gen_ai.request.model` is present on some tool spans and absent on others

In one turn that called two tools, the `execute_tool Bash` span carried `gen_ai.request.model` and
`dash0.gen_ai.request.model.original` while the `execute_tool Read` span carried neither. Same
session, same turn, same model.

`sendToolTrace` fills the model from the saved trace context's `Model` field, and failing that by
re-reading the transcript with `transcript.ReadModel` (`internal/pipeline/pipeline.go:261`). So
whether the attribute resolves depends on what the turn had written by the time that hook ran, which
makes it a function of timing rather than of the tool.

## Reproduction

```sh
QA_MODEL=haiku qa/tools/qa-session.sh \
  'Run the bash command: echo qa-tool-probe. Then read the file settings.json in the .claude directory. Then reply with exactly the word done.' \
  finding-tool-model
```

Then read `gen_ai.request.model` off each `execute_tool` span. Observed on one run; not yet checked
for how often it reproduces or whether the order of the calls decides which one loses it.

## Why it matters

Grouping tool cost or latency by model silently omits the spans that lack the attribute. The
omission is partial and inconsistent, which is harder to notice than the attribute being absent
everywhere.

## Suggested fix

Decide whether a tool span should carry a model at all. It describes a tool execution, not a model
request, so absent-everywhere is a defensible answer and present-sometimes is not.
