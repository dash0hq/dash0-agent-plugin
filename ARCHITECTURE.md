# Architecture

The plugin starts a short-lived Go binary for each coding-agent hook event.
It keeps session state on disk and exports OTLP JSON over HTTP. It is not a
daemon, and the four runtimes do not expose equivalent data.

## Event flow

```text
Runtime hook registration
  -> runtime bootstrap, selects a cached release binary
  -> cmd/<runtime>-on-event, reads stdin and runtime configuration
  -> runtime normalization and usage recovery
  -> internal/pipeline, manages session/turn state and builds spans
  -> internal/otlp, applies export privacy controls and sends telemetry
```

The bootstraps download and verify a release on a cache miss. A source checkout
does not automatically run the Go code in that checkout. For local development,
follow the local-development steps in the runtime's developer README. They
differ per runtime, and the cache path depends on how the plugin was installed.

Entrypoints read one JSON payload with `pipeline.ReadEvent`. Claude feeds its
already canonical event shape into the pipeline. Cursor, Codex, and Copilot
translate runtime fields and event names before calling `pipeline.Process`.
Normalizers can return `nil` to drop events that have no consumer or duplicate
another event.

`Process` manages trace context, writes the session event log, dispatches the
event, and returns messages for the entrypoint to render. Rendering stays
runtime-specific because stdout and stderr have different meanings to each CLI.
Copilot also exports recovered native tool and sub-agent spans from its
entrypoint after the pipeline processes the turn.

## Package ownership

| Path | Responsibility | Change here when |
| --- | --- | --- |
| `cmd/*-on-event/` | Hook input/output, configuration wiring, runtime orchestration and utility subcommands | The CLI invocation or hook response contract changes |
| `internal/source/` | Runtime-specific payload translation and readers for runtime data | A runtime changes its hook fields, rollout format, native telemetry, or auth signals |
| `internal/pipeline/` | Session and turn lifecycle, event dispatch, span construction and shared extractors | A normalized event needs different lifecycle behavior or span attribution |
| `internal/otlp/` | Wire types, trace context persistence, attribute helpers, privacy controls and HTTP export | The exported representation, privacy behavior, or transport changes |
| `internal/harness/` | Runtime names, option precedence, credential resolution and state-directory conventions | Configuration behavior shared by entrypoints changes |
| `internal/config/`, `internal/dotenv/` | Local Markdown config parsing and `.env` loading | File configuration syntax or loading changes |
| `internal/transcript/` | Claude transcript readers | Claude turn usage, model, response, or skill extraction changes |
| `internal/filelog/` | Session event JSONL storage and lookup | Cross-event lookup or event-log storage changes |
| `internal/identity/`, `internal/vcs/`, `internal/sessionurl/` | Identity, repository metadata and Dash0 session links | Metadata detection or session URL derivation changes |
| `<runtime>/` and runtime manifest directories | Shipped bootstraps, registrations, commands, skills and user documentation | Installation or runtime discovery changes |
| `scripts/`, `.github/workflows/` | Repository build/release tooling and CI | Verification or release mechanics change |
| `internal/demo/`, `cmd/demo/` | Synthetic demo telemetry | Demo data or generation changes, not production hook processing |
| `internal/version/` | The `Version` variable, stamped at build time by GoReleaser through ldflags. The source value is `dev` | Almost never. Release versions come from `scripts/version.sh`, not from this file |
| `test/` | E2E, contract, consistency and capture tests | A test of a hook, install or release contract is added or changed |
| `qa/` | Live QA specs, drivers and learnings, run through the engineering plugin | Live product QA changes; follow [qa/AGENTS.md](qa/AGENTS.md) |

These are placement guidelines, not a claim of strict dependency isolation.
The pipeline's canonical vocabulary comes from Claude, and it still calls
Claude transcript and billing readers. `internal/otlp` also owns persisted trace
context. Follow existing callers before moving behavior between packages.

## Runtime data is different

| Runtime | Lifecycle and tool events | Usage source |
| --- | --- | --- |
| Claude Code | Hook payloads in the canonical vocabulary | Claude transcripts |
| Cursor | Normalized generic tool hooks; duplicate specialized hooks are dropped | `afterAgentResponse` hook payload |
| Codex | Normalized hooks; tool duration can be reconstructed from matching events | Codex rollout files |
| GitHub Copilot CLI | Hooks drive lifecycle; native OTel supplies tool and sub-agent spans | Copilot's native OTel files |

Do not infer support in one runtime from support in another. Missing measurements
are not measured zeroes. Copilot's native OTel input cannot independently prove
the token measurement that the plugin copies. Cursor's transcript does not carry
the token counts needed to validate its hook usage independently.

[FEATURE_MATRIX.md](FEATURE_MATRIX.md) records support differences.
[DEVELOPMENT.md](DEVELOPMENT.md#telemetry-attributes) defines the exported attributes.
The runtime developer READMEs and `qa/learnings/` explain the observed limitations.

## Contracts to preserve

- Observational hooks log failures without returning a blocking exit status.
  Utility subcommands and installers can fail non-zero; do not apply the hook
  rule to every command.
- Disk state joins events across fresh processes. `UserPromptSubmit` starts a
  top-level turn's trace, and tool and sub-agent events attach to it. When received,
  `SessionEnd` removes the pipeline's per-session scratch directory. Copilot's
  session-start markers and native-OTel consumption cursors live outside it and
  must survive that cleanup. Codex exposes no `SessionEnd`, so this path does not
  reclaim its scratch state. `SessionEnd` is not the only deleter. On Copilot,
  `cmd/copilot-on-event` removes the scratch directory of a suppressed session,
  and `copilot.SweepOldSessionDirs` runs at `SessionStart` and deletes any marked
  session directory idle for more than `staleFileTTL`, a live idle session
  included. Cross-turn state kept in the scratch directory must survive losing
  the directory. Handle ordering and agent reuse explicitly; a session is not
  necessarily one turn or one process.
- Session IDs reach filesystem paths. Reuse the existing safe-ID helpers before
  accessing or deleting session directories, and preserve runtime-specific
  reserved-directory protections.
- `harness.Config` centralizes option resolution. Ordinary options and secrets
  deliberately have different fallback rules. The first file lookup memoizes the
  selected Markdown configuration per harness for the process lifetime, so later
  working-directory changes cannot select a different file. Environment-backed
  options are resolved when their accessors run; `Config()` itself is not memoized.
- `omit_io` defaults to true. Keep content redaction and user-identity controls
  intact when adding export paths. The scratch event log is not the exported
  payload; do not assume export redaction makes local files safe to share.
- Keep token ownership and span parenting explicit. Repeating aggregate usage
  on a child span can double-count totals. Validate turn boundaries, resumed
  sessions and sub-agents when changing attribution.

See [DEVELOPMENT.md](DEVELOPMENT.md#verification) for which tests exercise these
contracts, and [qa/AGENTS.md](qa/AGENTS.md) before running live QA.
