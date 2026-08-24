# JSON output from a span query is capped at 100 records

`dash0 spans query -o json --limit 500` fails outright with `json output is limited to
100 records; use --limit 100 or lower, or choose a different output format`. CSV and
table output take a higher limit; only the OTLP/JSON form is capped.

A hard error is the good case. The bad case is a session with more than 100 spans and
`--limit 100`: the query succeeds, returns exactly 100, and every count derived from it
is a floor rather than a total.

**Why it matters:** span counts are the primary assertion of a QA run. Truncation makes
a session look like it lost telemetry, and the shortfall grows with session size, so it
appears exactly when a run gets interesting.

**How to apply:** keep `--limit 100` for JSON, and treat a result *equal* to the limit as
truncated rather than complete. `qa/tools/qa-compare.py` prints a warning in that case.
For a session that needs more, query it in time slices, or use CSV with explicit
`--column` flags.
