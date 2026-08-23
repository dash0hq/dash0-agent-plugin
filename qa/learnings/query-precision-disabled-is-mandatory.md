# A span query without --precision disabled can drop spans

`dash0 spans query` samples adaptively by default. For a narrow filter such as one
`gen_ai.conversation.id`, sampling can return a subset, and the CLI's own help documents
`--precision disabled` for exactly this case: "Disable adaptive sampling so a narrow
filter always returns every match".

**Why it matters:** a dropped span is indistinguishable from a span the plugin never
sent. A QA run whose whole purpose is comparing span counts against an independent
expectation will report a false finding, and the finding will not reproduce on the next
query.

**How to apply:** put `--precision disabled` on every QA query, without exception. Pair
it with [[query-json-output-caps-at-100-records]], which is the other way the same query
silently under-reports.
