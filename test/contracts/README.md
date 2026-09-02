# Release planning contracts

One script, covering `scripts/release-plan.sh`, `scripts/version.sh` and
`scripts/expected-artifacts.sh`. The Release workflow cannot be dispatched from
a PR, so its branching gets no other pre-merge coverage.

| Script | Contracts | Needs |
|---|---|---|
| `release.sh` | every Release dispatch resolves to the right version, tag and bump, and the guarded combinations are refused · the artifact list follows `.goreleaser.yaml` · a bump rewrites all thirteen pins | jq, git |

```bash
./test/contracts/release.sh
```

CI runs the same line in the `release-checks` job.

## Where the rest went

This directory used to hold the install, credential and bootstrap contracts as
shell, one script per runtime. Each covered whichever runtime its author had in
hand, so a check that named one file only ever guarded that file. They are Go
packages now:

| Was | Is | Runs in |
|---|---|---|
| `cursor.sh`, `codex.sh` (install) | [`test/installers`](../installers/README.md), which runs the real installer against a throwaway home and parses what comes out | `go test ./...`, all three operating systems |
| `claude.sh` (install) | [`test/marketplaces`](../marketplaces), because Claude has no installer script — it installs through the CLI's own marketplace verbs | `make test-marketplaces`, ubuntu only, behind the `marketplace` tag because it needs the real CLI |
| the credential half of all three | [`test/credentials`](../credentials), which drives each entrypoint and reads the Authorization header off a real request | `go test ./...`, all three operating systems |
| `bootstrap.sh` | [`test/consistency`](../consistency), driven off one `Agents` table so every check runs against all four runtimes, and against the `.ps1` bootstraps as well as the `.sh` ones | `go test ./...`, all three operating systems |

One half did not move. `claude.sh` also checked that
`claude plugin install --config` persists a token, and nothing covers that now,
on purpose: the plugin deliberately does not use `--config`, because it stores
the token in the OS secrets store, which on macOS means a Keychain no throwaway
`HOME` can hold. `test/installers/README.md` records the same decision.

`release.sh` stays in shell because what it drives is shell, and because it
needs neither Go nor a runtime CLI: `jq`, `git`, and three seconds.

`lib.sh` still carries `os_arch`, `force_https`, `skip_or_fail` and
`start_mock_otlp`, which nothing sources any more. `release.sh` uses only
`$REPO` and `_cleanup`.
