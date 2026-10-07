# Repository guidance

This repository exports coding-agent activity as OpenTelemetry. It ships four
runtime integrations backed by shared Go code. Read [ARCHITECTURE.md](ARCHITECTURE.md)
before changing package ownership or event processing, and
[DEVELOPMENT.md](DEVELOPMENT.md) for coding conventions and verification commands.

## Before changing code

- Read the developer README for the affected runtime: [Claude](claude/README.md),
  [Cursor](cursor/README.md), [Codex](codex/README.md), or [Copilot](copilot/README.md).
- Keep runtime payload translation in `internal/source/<runtime>/`, hook I/O and
  runtime orchestration in `cmd/<runtime>-on-event/`, and shared lifecycle behavior
  in `internal/pipeline/`. The architecture document describes the existing exceptions.
- Keep shipped commands, skills, and hook registrations under their runtime
  directory. Do not create root `commands/`, `skills/`, or `hooks/` directories;
  runtimes can auto-discover each other's files there.
- Preserve observational hooks' fail-open behavior. Telemetry failures must not
  block the user's agent session. Install-time and utility subcommands have
  separate exit contracts.
- Preserve privacy defaults and credential precedence. Use the shared `harness`
  configuration accessors rather than adding independent environment lookups.
  When adding a user-facing plugin option, update Claude's `userConfig` in
  `.claude-plugin/plugin.json` and its option table in `.claude-plugin/README.md`;
  hosted consistency checks enforce both.
- Each hook runs in a new process. Session state on disk, event ordering, and
  concurrent hook invocations matter; do not replace cross-event state with
  process-local variables.

## Verification and documentation

- Run targeted tests while iterating and `make ci` for the local lint/test set.
  It is not the entire hosted CI workflow. See [DEVELOPMENT.md](DEVELOPMENT.md#verification)
  for contracts, cross-platform checks, and live tests that need credentials.
- Add a regression test for changed behavior, including the affected runtime's
  normalization and emitted telemetry where applicable.
- Update [FEATURE_MATRIX.md](FEATURE_MATRIX.md) when runtime support changes.
  Update the telemetry reference in `DEVELOPMENT.md` when attributes change, and
  runtime install/configuration docs when their contracts change.
- Use `scripts/version.sh` for coordinated version changes. Do not bump individual
  manifests or bootstrap pins by hand. Follow the documented release workflow;
  changing code does not authorize publishing a release.
- Work under `qa/` also follows [qa/AGENTS.md](qa/AGENTS.md). Live QA uses the
  setup and specs there, not an ad hoc production experiment.
