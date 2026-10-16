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

## Skills loaded from files

Native `<skill>` injections continue to be attributed on the turn's chat span.
Codex hosts that load skills through ordinary tools also report
`dash0.gen_ai.tool.skill.name` and `.source` on the successful read's existing
`execute_tool` span. The name comes from the returned YAML frontmatter; this
tool-driven route has source `model`, like Claude Code's `Skill` tool. It records
a file load, not proof that every instruction in the skill was followed.

Recognition requires one literal `SKILL.md` path and returned frontmatter with
string `name` and nonempty string `description`. Supported reads are `Read` /
`read_file`, and `Bash` / `exec_command` running a single `cat`, `head [-n N]`,
or `sed -n '1,Np'` command. Quoted paths, relative paths, Windows file-reader
paths and CRLF frontmatter are supported. Home-relative `~/` paths are accepted.
Variable/glob/brace expansion, comma-separated paths, pipelines, command lists,
scripts, concatenated files and outputs without complete frontmatter are
deliberately omitted. The hook never opens the referenced file itself.
Shell paths containing `,`, `(`, `)`, `#` or `^` are also omitted, even inside
quotes, to avoid interpreting argument lists or extended glob syntax.

The desktop `Bash` route uses plain stdout and has been verified against actual
hook payloads. Other host shapes are best-effort: `exec_command` accepts an
`output` string with a successful `exit_code`; file readers accept plain text,
`content` / `text`, or `file.content`, starting at the beginning of the file.
Line-numbered text and shell-report headers are not stripped. Names are exactly
the frontmatter names; a native loader may qualify them with a plugin prefix.

Failed or unfinished reads and catalogue mentions do not count. Each observed
successful read has its own span, so rereading a file counts again; these are
load counts, not unique skill invocations. Nothing is copied onto the chat span
from this route. Native injections keep their existing last-skill-per-turn
behavior. `omit_io` / `omit_user_info` do not remove the two skill attributes,
and no new attribute contains the file path or skill contents.

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
