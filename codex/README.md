# OpenAI Codex source — developer reference

This directory holds everything shipped to Codex — the hook registration
(`hooks.json`) and the bootstrap wrapper (`codex-on-event.sh`). This file is the
developer reference: how to build and run local changes.

End-user install / configure / uninstall docs live in
[.codex-plugin/README.md](../.codex-plugin/README.md). Releasing is shared
across runtimes — see [DEVELOPMENT.md](../DEVELOPMENT.md#releasing).

## Adding or removing a hook event — update both lists

The two install paths enumerate the event set separately:

| Path | Reads |
|---|---|
| `codex plugin add` (marketplace) | `codex/hooks.json`, via `.codex-plugin/plugin.json` |
| `install-codex.sh` | `codex.HookEvents` in `internal/source/codex/trust.go`, rendered by `emit-codex-hooks` |

The installer cannot read `codex/hooks.json` — it only fetches the bootstrap
script — and it needs Go regardless, because every hook requires a
`trusted_hash` derived from install-time values (the absolute command path, and a
group index that depends on the user's own existing hooks). Codex ignores a hook
whose hash does not match, without a prompt.

Change one list and you must change the other, or that install path silently
stops instrumenting the event. `TestCodexHookEventsMatchManifest` in
`test/consistency` fails when they diverge. A new event also needs its
`HasMatcher` value checked against real Codex behaviour — see the notes in
`trust.go`.

## Build & run locally

Wire once against your local build, then rebuild-and-run.

```bash
# BIN = the exact path the bootstrap execs (build here and it runs your code, no download).
# BOOT = the working-copy bootstrap the config.toml hooks invoke.
BIN="$HOME/.local/state/dash0-agent-plugin/codex/bin/codex-on-event-$(grep '^VERSION=' codex/codex-on-event.sh | cut -d'"' -f2)-$(uname -s | tr A-Z a-z)-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
BOOT="$PWD/codex/codex-on-event.sh"

# 1. build the binary to that path
make build-binary PKG=./cmd/codex-on-event OUT="$BIN"

# 2. credentials, with debug so you can see spans without a backend
cat > ~/.codex/dash0-agent-plugin.local.md <<'EOF'
---
otlp_url: "https://ingress.<region>.aws.dash0.com"
auth_token: "<token>"
debug: true
debug_file: "/tmp/dash0-codex-debug.log"
---
EOF

# 3. register hooks + trust in config.toml, pointing at your working-copy bootstrap (run once)
"$BIN" emit-codex-hooks --command "$BOOT" --config ~/.codex/config.toml >> ~/.codex/config.toml
```

Then run and watch spans:

```bash
codex exec 'run: echo hi' </dev/null      # or interactive `codex` — start a NEW session
tail -F /tmp/dash0-codex-debug.log        # each span logged as [dash0:trace] {...}
```

Rebuild loop — just step 1, then a new session. No re-trust: the trust hash is over the hook *command* (the bootstrap path), so editing the bootstrap or the Go binary is picked up without touching `config.toml`. (`</dev/null` keeps `codex exec` from blocking on stdin.)
