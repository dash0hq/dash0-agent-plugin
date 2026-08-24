# Install / config contracts

Executable contract tests for the plugin install, credential-delivery, and
uninstall flows — the behaviour the runtime READMEs' *Installation* and
*Configuration* sections depend on. They run **locally** (while iterating on an
installer) and in CI (the `install-config-contract` job just calls these).

| Script | Contracts | Needs | Local? |
|---|---|---|---|
| `claude.sh` | settings.json ≠ install · `--config` credential storage · creds → OTLP | `claude` CLI, network, go/jq/curl | mostly anywhere; the credential-storage contract is **Linux-only** (see below) |
| `cursor.sh` | creds → OTLP · install layout + hooks merge · uninstall strip | network (install/uninstall resolve the latest release), go/jq/curl | yes |
| `codex.sh`  | creds → OTLP · install merge + pre-trust · uninstall strip | go/jq/python3/curl | yes (no codex CLI) |
| `opencode.sh` | creds → OTLP · corrupted-binary discard · install layout · uninstall strip | network (install/uninstall resolve the latest release), go/jq/curl | yes (no opencode CLI) |

## Run

```bash
./test/contracts/run.sh            # all four
./test/contracts/run.sh codex      # one agent
```

Each script is hermetic — it uses throwaway `HOME`s under `/tmp` and a mock OTLP
server on `:4319`, so it never touches your real `~/.claude` / `~/.cursor` /
`~/.codex` / `~/.config/opencode`.

## Notes

- **The `claude.sh` credential-storage contract is Linux-only.** It pins *where*
  `claude plugin install --config` persists credentials (non-sensitive →
  `settings.json`, `AUTH_TOKEN` → the secrets store, with a `.credentials.json`
  fallback on Linux). macOS uses the Keychain and a different layout, so it
  **skips** off Linux; CI (Linux) validates it.
- **`opencode.sh` skips its install and uninstall contracts until
  `install-opencode.sh` exists.** That is a plain skip, in CI too, because the
  script is a required CI step and the installers are still to be written. Once
  they land, turn that branch into `skip_or_fail` like the others.
- The `cursor.sh` install/uninstall contracts download the latest published
  release's Cursor binary, so they need network and an existing release. If the
  release can't be resolved they **skip locally but fail in CI** (`$CI` set) —
  a silently skipped contract would report green while testing nothing.
