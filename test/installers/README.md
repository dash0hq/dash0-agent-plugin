# Installer tests

Go tests that drive the cursor and codex install and uninstall scripts as
subprocesses and assert what they leave on disk. Each script exists twice, and
`scriptExt()` picks the flavour the host ships: the `.sh` pair on POSIX, the
`.ps1` pair on Windows. So the windows-latest leg of `build-test` is the only
place `install-cursor.ps1` is *run*; `install-codex.ps1` is also run by the
windows leg of the `e2e` job, which needs credentials and so does not cover fork
PRs. [`test/consistency`](../consistency) reads both on every platform — they are
ASCII, they use none of the 5.1-incompatible constructs a textual rule can find,
and they parse everywhere a PowerShell is installed, which excludes the macOS
runner — and that is what catches the drift written on a laptop.

They pin the behaviour each runtime README's *Installation* section depends on,
so a wrong doc assumption fails CI rather than a rollout.

They run in `make test` — no build tag, no network, no agent CLI.

## Scope

These cover the install logic that lives in **shell** and has no Go caller. Two
implementations per row, so a fix in one is not a fix in the other:

| File | Under test | POSIX | Windows |
|---|---|---|---|
| `codex_test.go` | the strip of the managed block in `~/.codex/config.toml`, asserted on uninstall | awk | a line filter |
| `cursor_test.go` | the merge into `~/.cursor/hooks.json`, and the strip on uninstall | jq | `ConvertFrom-Json` |

Both rows also assert that the credentials passed to the installer arrive in the
`dash0-agent-plugin.local.md` the hook parses.

## How they stay hermetic and offline

Each test points `HOME` and `XDG_STATE_HOME` at `t.TempDir()`, so it never
touches the developer's `~/.cursor` or `~/.codex`. Both installers then take
their offline paths:

- The binary is built from this checkout and pre-staged at the version-pinned
  path the installer resolves, so its download branch is skipped. Building
  happens in the test process, under the real `HOME`, because a Go toolchain
  resolved through `$HOME` (asdf, mise) cannot run once the installer has a
  temp one.
- `DASH0_VERSION` is pinned, so neither installer queries the GitHub releases
  API.
- `DASH0_SOURCE_DIR` makes both installers take their content files from the
  checkout instead of the tagged release ref: the whole plugin tree for Cursor,
  the bootstrap for Codex. Without it a test would assert against the **last
  release's** files rather than the branch's.

Both installers handle their on-disk files the same way, which is what lets the
two tests set up identically:

| Artifact | Rule |
|---|---|
| the binary | path is version-pinned, so an existing one is reused |
| content files (bootstrap, and Cursor's plugin tree) | always written, never reused |

The split matters. A bootstrap resolves the binary it execs from the `VERSION` it
declares, and neither installer version-pins the path it writes it to, so reusing
one would pin the plugin to an old release while the installer reported success.
Always writing is what keeps that impossible.

Both tests therefore seed a bootstrap declaring `0.0.1-stale` before the install
and assert the file afterwards declares the version being installed. Without that
seed the assertion is vacuous, because a clean directory gets a fresh copy either
way.

## Requirements

`jq` on POSIX, because `install-cursor.sh` requires it to merge hooks.json.
Nothing extra on Windows: the `.ps1` pair merges with `ConvertFrom-Json`, and
`powershell` ships with the OS.

## Not covered here

- **Credential delivery.** `dash0-agent-plugin.local.md` is parsed by the
  binary, so the parse is a Go unit test and the resolution order is covered by
  `internal/harness`. That a configured token reaches the Authorization header of
  a real request is [`test/helpers/hookcheck`](../helpers/hookcheck), driven
  from each `cmd/*-on-event` package, for all four runtimes and both sources.
- **Claude and Copilot.** Neither ships an `install-*` script; both install
  through their CLI's own marketplace verbs, which is
  [`test/marketplaces`](../marketplaces).
- **`claude plugin install --config`.** The plugin deliberately does not use
  `--config`: it stores the token in the OS secrets store, which on macOS means
  the Keychain, outside any throwaway `HOME`.
- **Claude install behaviour.** `test/marketplaces/claude_test.go` covers the
  install path and the negative case where `settings.json` alone must not
  install.
