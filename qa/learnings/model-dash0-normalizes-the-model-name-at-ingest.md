# Dash0 normalizes the model name at ingest and keeps the original in a second attribute

The plugin sends the dated snapshot id from the transcript, so a span is named
`chat claude-haiku-4-5-20251001`. Dash0 stores the canonical `claude-haiku-4-5` in
`gen_ai.request.model` and preserves what it was sent in `dash0.gen_ai.request.model.original`. The
span name is left as the plugin wrote it.

So a span carries the same model under three keys, in two spellings. `internal/otlp/otlp.go` maps
the pipeline's `model` field straight to `gen_ai.request.model`, which means the short spelling is
not something the plugin produces.

**Why it matters:** the mismatch between the span name and the attribute looks exactly like the
plugin writing one value in one place and a different value in another, and that reads as a defect.
It is not one. An earlier run recorded it as a product bug on the strength of the two values alone;
`dash0.gen_ai.request.model.original` is the attribute that settles it, and reading it first would
have saved the write-up.

**How to apply:** compare model *sets* after dropping a trailing `-YYYYMMDD`, which is what
`qa/tools/qa-cost.py` does in `canonical()`. When a model name looks wrong anywhere, read
`dash0.gen_ai.request.model.original` before concluding the plugin sent it that way. Related:
[[cost-is-reproducible-from-the-transcript-and-a-price-table]], where the same normalization decides
which row of the price table applies.
