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

