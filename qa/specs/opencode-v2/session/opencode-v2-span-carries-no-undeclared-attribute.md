---
id: opencode-v2-span-carries-no-undeclared-attribute
area: opencode-v2/session
runtime: opencode-v2
status: active
input: any opencode run directory; run it on every other spec's run
duration: ~5s per run
settling: 25s
cleanup: keep
covers:
  - internal/source/opencodev2/opencodev2.go
  - internal/otlp/otlp.go
  - DEVELOPMENT.md
---

## Given

Any run of the other specs in this area. The failure and privacy-off runs matter most: they reach
fields a healthy default run does not.

## When

```sh
for run in qa/runs/spec-opencode-*; do qa/tools/qa-attrs.py "$run"; done
```

## Expectation

`baseEvent`, the turn and tool events in `opencode.go` build an explicit map, and the shared
exporter copies every key nobody denied. Every key that reaches a span must be in the attribute
tables of `DEVELOPMENT.md`. Every other runtime's first pass found raw keys leaking this way.

## Oracle

`qa/tools/qa-attrs.py`, which reads what Dash0 stored against `DEVELOPMENT.md` and separates keys
added at ingest.

## Then

- `qa-attrs.py` exits `0` on every run: `Every attribute is in the documented contract.`

## Tolerance

**Keys added at ingest are listed, not counted.** `dash0.operation.*`, `dash0.resource.*` and
`dash0.span.name` are Dash0's.
