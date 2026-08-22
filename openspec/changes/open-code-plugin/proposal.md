## Why

OpenCode is the fifth coding agent our users run, and the only one of the five
with no Dash0 telemetry today. Sessions there are invisible: no token usage, no
tool-call traces, no cost or identity attribution — while Claude Code, Cursor,
Codex, and Copilot CLI all report through one shared Go pipeline.

OpenCode also exposes more per-turn detail than any runtime we support so far
(exact token counts including cache read/write and reasoning, native tool
timings, real MCP server identity, session titles, sub-session parenting, and a
user-visible toast channel), so a port is not just coverage — it closes gaps
that Cursor and Codex cannot.

It is additionally the first runtime that can be driven **headlessly**
(`opencode run`), which makes it the first one whose plugin can be tested end to
end in CI against a real agent process rather than synthesized hook payloads.

## What Changes

- **New runtime: OpenCode.** A fifth entrypoint `cmd/opencode-on-event` plus a
  normalizer `internal/source/opencode` translating OpenCode's event vocabulary
  into the pipeline's canonical one. `internal/pipeline` and `internal/otlp` are
  reused unchanged, so span shape and attributes are identical by construction.
- **A TypeScript plugin as the event source.** Unlike the other four runtimes,
  OpenCode has no stdin-shell-hook mechanism: plugins are in-process TS modules
  (`@opencode-ai/plugin`) loaded by Bun. `opencode/` ships a plugin that
  subscribes to the OpenCode event bus and tool hooks, filters to the events the
  pipeline consumes, and pipes the canonical JSON into the shell wrapper.
- **A fifth shell wrapper, not a TypeScript reimplementation.**
  `opencode/opencode-on-event.sh` is cloned from `claude/claude-on-event.sh` and
  owns exactly what the other four wrappers own: frontmatter config parsing,
  macOS keychain resolution, OS/arch detection, release download, checksum
  verification, and fail-open. The plugin knows none of it — it translates
  events and writes JSON to the wrapper's stdin.
- **Two install paths.** An npm package (`@dash0/opencode-plugin`, referenced
  from `opencode.json`'s `plugin` array) as the native path, plus
  `install-opencode.sh` / `uninstall-opencode.sh` for users who cannot or will
  not pull from npm.
- **Configuration parity.** All `.local.md` frontmatter keys the other runtimes
  support, project-scope-over-user-scope, `OPENCODE_PLUGIN_OPTION_*` /
  `DASH0_*` precedence, plus macOS keychain token lookup
  (`auth_token_keychain_service` / `_account`) — until now Claude-only.
- **User-visible session link.** The `dash0: connected → <session link>` banner
  rendered through `client.tui.showToast`. Claude Code is the only other runtime
  that can show it.
- **Ported commands and skill.** `dash0-configure`, `open-session`, and
  `audit-usage` as OpenCode commands. `audit-usage` is rewritten against
  OpenCode's own message storage rather than Claude's JSONL transcripts.
- **Reasoning tokens reported.** OpenCode reports them per assistant message, so
  the normalizer sets `gen_ai.usage.reasoning.output_tokens` on the event. The
  attribute layer passes unknown event keys through, and Copilot already sets the
  same key from its entrypoint, so this needs no change to `internal/otlp`.
- **A live-session test layer that does not exist today.** A hermetic
  `opencode run` driven against a mock LLM provider and the existing mock OTLP
  server, asserting spans produced by a real agent process through the real
  plugin, wrapper, and binary — plus a documented Dash0 verification recipe that
  reads the same session back through the Dash0 MCP tools.
- **Release wiring.** A fifth goreleaser build id, an npm publish job, and
  version lockstep between the npm package and the Go release assets.
- Docs: `FEATURE_MATRIX.md` gains a fifth column, `DEVELOPMENT.md` a fifth
  per-runtime guide link, and `opencode/README.md` the local-dev and
  telemetry-verification instructions.

## Capabilities

### New Capabilities

- `opencode-plugin`: Observability for OpenCode sessions — which OpenCode events
  become which spans, how the TS plugin bridges to the Go binary, how the plugin
  is configured and installed, how it fails open, and what it shows the user.

### Modified Capabilities

None. `openspec/specs/` is currently empty, so there is no existing spec whose
requirements change. The shared pipeline's span contract is documented in
`DEVELOPMENT.md#telemetry-attributes` and is deliberately **not** modified by
this change — the new runtime conforms to it rather than altering it.

## Impact

**New code**

- `cmd/opencode-on-event/` — entrypoint (mirrors `cmd/cursor-on-event`).
- `internal/source/opencode/` — event normalizer + golden tests.
- `opencode/` — TS plugin package, `opencode-on-event.sh`, commands, skill, README.
- `install-opencode.sh`, `uninstall-opencode.sh`.
- `test/capture/opencode/`, `test/contracts/opencode.sh`,
  `test/consistency/opencode_test.go`, `test/e2e/opencode_e2e_test.go`,
  `test/live/opencode/` (mock LLM provider + headless session runner).

**Modified code**

- `internal/harness/harness.go` — a fifth `Harness` value (`OpenCode`).
- `.goreleaser.yaml`, `.github/workflows/release.yml` — fifth binary + npm publish.
- `.github/workflows/ci.yml` — the contract and live-session jobs.
- `test/contracts/run.sh` — a fifth target.
- `FEATURE_MATRIX.md`, `DEVELOPMENT.md`, `README.md`, `Makefile`.

**Dependencies**

- New: `@opencode-ai/plugin` (peer/dev), Bun or Node ≥ 20 at runtime — both
  already present wherever OpenCode runs. No new Go dependencies.
- New release secret: `NPM_TOKEN`.
- The live-session test needs the `opencode` CLI on the runner; it is skipped
  locally and failed in CI when absent, following the existing contract-test rule.

**Risk**

- OpenCode's plugin API is pre-1.0 and its `Hooks` surface changes between minor
  versions; the plugin must pin a supported range and fail open on shape
  mismatches.
- Per-event process spawn is the same cost model as the other runtimes, but
  OpenCode's bus is far chattier — the plugin, not the wrapper, is responsible
  for filtering, and getting that filter wrong is the main performance risk.
- A fifth near-duplicate bash wrapper deepens existing duplication. Accepted
  deliberately over a fifth copy in a second language; see design.md Decision 6.
