# A pre-0.1.25 Cursor wrapper overwrites the QA token with the developer's own

The `cursor` runtime configures the installed plugin through `CURSOR_PLUGIN_OPTION_*`, which
`internal/harness` ranks above the configuration file. A wrapper from before 0.1.25 defeats
that, because it reads the configuration file itself and re-exports every value — including

```sh
[[ -n "$val" ]] && export CURSOR_PLUGIN_OPTION_AUTH_TOKEN="$val"
```

which is the same high-precedence form. So the QA token is replaced by whatever
`~/.cursor/dash0-agent-plugin.local.md` holds, and the session exports to the QA endpoint with
the developer's production credential.

Measured 2026-09-01 against the v0.1.19 wrapper on a developer machine: 6 hooks recorded, both
spans built and written to `plugin-debug.log`, every export rejected, **zero spans in Dash0**.
`qa-compare.py` reported `chat: Dash0 has 0, the hooks imply 1` and the run read as total
telemetry loss. The plugin was blameless; the endpoint override had worked and only the token
had not, because the URL is exported as low-precedence `DASH0_OTLP_URL` and the token is not.

Configuration moved into Go after 0.1.24, so the shipped wrapper exports nothing and cannot do
this.

**Why it matters:** the symptom is indistinguishable from a broken export, and a developer
machine several releases behind the working tree is the normal case, not the exception.

**How to apply:** `qa-session-cursor.sh` compares the registered wrapper against
`cursor/cursor-on-event.sh` byte for byte and refuses to run when they differ — comparing the
file rather than the `VERSION=` string, so a hand-edited wrapper at the right version is caught
too. The remedy it prints is a re-install. `QA_CURSOR_ALLOW_STALE=1` overrides it for the one
case where testing the old install *is* the question; the manifest records
`wrapper_matches_shipped: false` and `qa-compare.py` prints a warning above the counts.

QA cannot dodge this by registering its own wrapper at project scope. Cursor honours the
user-scope and project-scope hook files together, so a second registration emits every span
twice.
