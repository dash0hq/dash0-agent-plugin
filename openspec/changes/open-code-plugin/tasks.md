## 1. Capture and confirm OpenCode's real behavior

Design decisions 3, 4 and 10 rest on assumptions taken from the SDK types rather
than observation. Resolve them before writing anything that depends on them.

- [x] 1.1 Add `test/capture/opencode/` (mirroring `test/capture/cursor/`) with a throwaway plugin that appends every bus event and hook call to a JSONL file; verify by running one real `opencode` session and confirming the file contains events for a prompt, a tool call, an MCP tool call, and a delegated sub-agent
- [x] 1.2 Record the capture as `internal/source/opencode/testdata/captured_events.jsonl`; verify it contains at least one root session, one child session with `parentID`, one failing tool call, and one multi-step turn
- [x] 1.3 Confirm whether `session.idle` fires for child sessions and record the finding in `opencode/README.md`; if it does not, fall back to the child's last completed assistant message as the `SubagentStop` trigger and record that
- [x] 1.4 Confirm how OpenCode names MCP-provided tools in `ToolPart.tool`; verify by locating an MCP call in the capture and recording the exact string, so the normalizer can rewrite it to the canonical `mcp__<server>__<tool>` form the shared extractor expects
- [x] 1.5 Confirm whether a terminal `message.part.updated` for one `callID` can arrive more than once; verify by counting terminal-status occurrences per call id in the capture
- [x] 1.6 Confirm where OpenCode stores session messages on disk (for the `audit-usage` port) and record the path and format in `opencode/README.md`
- [x] 1.7 Confirm OpenCode accepts a custom OpenAI-compatible provider via `provider.<id>.options.baseURL` with a dummy `apiKey`; verify by pointing `opencode run` at a throwaway localhost server and observing the request arrive. This gates the whole live-test layer — if it fails, apply the fallback in design.md Risks before building task group 7

## 2. Go side: harness, entrypoint, normalizer

- [x] 2.1 Add `harness.OpenCode` (`Name: "opencode"`, `EnvPrefix: "OPENCODE"`, `DataSubdir: "opencode"`, empty `Provider`) and verify `go test ./internal/harness/` passes with a case asserting the constants and the `DataDir` precedence chain
- [x] 2.2 Create `internal/source/opencode/opencode.go` implementing the Decision 3 mapping table, including the `agent_id` / `agent_type` fields from Decision 4 and the MCP tool-name rewrite from 1.4; verify with unit tests per mapped event kind
- [x] 2.3 Map OpenCode's token fields onto `gen_ai.usage.input_tokens`, `output_tokens`, `cache_read.input_tokens`, `cache_creation.input_tokens` and, when greater than zero, `gen_ai.usage.reasoning.output_tokens`; verify unit tests assert the reasoning key is absent at zero
- [x] 2.4 Set `gen_ai.conversation.name` from the OpenCode session title on chat events; verify a unit test asserts it is absent for an untitled session
- [x] 2.5 Create `cmd/opencode-on-event/main.go` following `cmd/cursor-on-event/main.go`: read stdin, chdir to the event cwd, normalize, `pipeline.Process`, log messages to stderr, exit 0 on error; verify `go build ./...` and a `main_test.go` that feeds a captured event and asserts a clean exit

## 3. Shell wrapper

- [x] 3.1 Create `opencode/opencode-on-event.sh` from `claude/claude-on-event.sh`, changing only the config paths (`.opencode/` and `~/.config/opencode/`), the secure var (`OPENCODE_PLUGIN_OPTION_AUTH_TOKEN`), the data-dir chain, and the binary name, and switching the failure mode to fail-open `exit 0`; verify `make shellcheck-lint` passes
- [x] 3.2 Verify the wrapper's config precedence by hand against the spec's scenarios: project file wins wholesale, `DASH0_*` fills non-secrets, `DASH0_AUTH_TOKEN` is never read, `enabled: false` exits silently
- [x] 3.3 Verify keychain resolution on macOS with a throwaway `security add-generic-password` item, including that a successful lookup overrides a literal `auth_token` and a failed one falls back to it
- [x] 3.4 Verify checksum enforcement by corrupting a cached binary and confirming the wrapper deletes it, never executes it, and exits 0

## 4. TypeScript plugin

The plugin translates events and writes JSON to the wrapper's stdin. It parses no
config, resolves no secrets, and downloads nothing (design.md Decision 6).

- [ ] 4.1 Scaffold `opencode/` as a dependency-free TS package with `@opencode-ai/plugin` as a peer/dev dependency and a bundle step producing a single `dash0-opencode-plugin.js`; verify the bundle imports nothing outside Node builtins
- [ ] 4.2 Implement the event filter and translator per the Decision 3 table, including the root-session resolution and cache from Decision 4 and the call-id dedupe set; verify a test replays `captured_events.jsonl` and asserts the exact sequence of canonical events produced
- [ ] 4.3 Implement the per-turn usage accumulator from Decision 5, flushed and reset on `Stop`; verify a test over a multi-step captured turn asserts the summed counts
- [ ] 4.4 Spawn `opencode-on-event.sh` fire-and-forget per canonical event, never awaited, stdin closed after the payload; verify a test asserts the handler resolves without waiting for the child and that a non-zero child exit is swallowed
- [ ] 4.5 Wrap every hook handler so an unrecognized event shape drops that event only; verify a test feeds malformed events and asserts subsequent well-formed events still translate
- [ ] 4.6 Render the wrapper's session-start message through `client.tui.showToast`, swallowing failure in headless mode; verify by running `opencode run` with telemetry configured and confirming no error surfaces
- [ ] 4.7 Emit `SessionEnd` on plugin shutdown so the scratch directory is freed; verify the session directory under the data root is gone after a session exits
- [ ] 4.8 Assert the spawn count for a recorded session in a test, so a future filter regression that spawns per streaming delta fails CI

## 5. Test layers that already exist

Each of these clones an established pattern rather than inventing one.

- [ ] 5.1 Add `internal/source/opencode/golden_test.go` replaying `captured_events.jsonl` into `golden_spans.json`, following `internal/source/codex/golden_test.go`; verify `make test` passes and that editing one attribute in the normalizer fails the golden
- [ ] 5.2 Add `test/consistency/opencode_test.go` asserting the OpenCode golden spans carry the same attribute keys as the Claude Code spans for equivalent events, following the existing consistency tests; verify it fails when an attribute is dropped from the normalizer
- [ ] 5.3 Add `test/e2e/opencode_e2e_test.go` (build tag `e2e`) following `test/e2e/copilot_e2e_test.go`: build the binary, feed canonical events, assert span shape against the `httptest` collector; verify `make test-e2e` passes
- [ ] 5.4 Add `test/contracts/opencode.sh` using `test/contracts/lib.sh` (`start_mock_otlp`, `skip_or_fail`, throwaway `HOME`) covering creds → OTLP, install layout, and uninstall strip, following `cursor.sh`; verify `./test/contracts/run.sh opencode` passes
- [ ] 5.5 Register `opencode` in `test/contracts/run.sh`'s target list and in the CI `install-config-contract` job; verify `./test/contracts/run.sh` runs all five

## 6. Installation and release

- [ ] 6.1 Add the `opencode-on-event` build id to `.goreleaser.yaml` and a step publishing `dash0-opencode-plugin.js` as a release asset; verify `goreleaser build --snapshot --clean` produces all five binaries and the JS asset
- [ ] 6.2 Stamp the pinned binary version into `opencode-on-event.sh` at release time exactly as the other four are stamped; verify the released script carries the tag and not a placeholder
- [ ] 6.3 Publish `@dash0/opencode-plugin` (containing the bundle and the wrapper) from the tagged release workflow with provenance and the `NPM_TOKEN` secret, versioned in lockstep with the Go release; verify a dry-run publish succeeds in CI
- [ ] 6.4 Write `install-opencode.sh` (download plugin + wrapper, write a config file if absent, no npm required) and `uninstall-opencode.sh` (remove plugin, wrapper, and cached binaries; leave the user config in place); verify both pass `make shellcheck-lint` and a round-trip leaves no plugin files behind
- [ ] 6.5 Verify the npm path end to end: add the package to `opencode.json`'s `plugin` array in a scratch project, run a session, confirm spans arrive
- [ ] 6.6 Verify the script path end to end on a machine with no npm registry access and confirm it produces the same spans as 6.5

## 7. Live-session test layer (new — OpenCode is the first runtime that can be driven headlessly)

- [ ] 7.1 Build `test/live/opencode/mock-llm/` — an OpenAI-compatible server scripting one deterministic turn: assistant text, a successful tool call, a failing tool call, a delegated sub-agent, and exact usage numbers including cache read/write and reasoning; verify `curl` against it returns the scripted response
- [ ] 7.2 Build `test/live/opencode/run.sh` driving `opencode run "<prompt>"` under a throwaway `HOME`/`XDG_STATE_HOME` with the mock LLM wired in via `provider.<id>.options.baseURL` and the plugin installed from the local checkout; verify it completes a turn without touching the developer's real `~/.config/opencode`
- [ ] 7.3 Point that run at the existing mock OTLP server (`start_mock_otlp` from `test/contracts/lib.sh`) and assert the collected spans exactly: span names, parent/child structure, and token values, which are knowable because the model response is scripted; verify the assertions fail when the normalizer drops an attribute
- [ ] 7.4 Assert the sub-agent structure specifically: an `invoke_agent` span under the chat span, with the child session's tool spans under the `invoke_agent` span and all of them on one trace id
- [ ] 7.5 Assert fail-open in the live harness: rerun with an unreachable endpoint, a rejected token, a malformed config file, and a corrupted cached binary, and confirm `opencode run` exits successfully each time with no error in its output
- [ ] 7.6 Add a `make test-live` target and a CI job gated on the `opencode` CLI being present, skipping locally and failing in CI when absent per the existing `skip_or_fail` rule; verify the job passes on a clean runner

## 8. Live Dash0 verification

Golden and consistency tests compare our output against our own expectations, so
they cannot catch a mapping that is wrong in both places. This layer is what
proves Dash0 actually received what we think it did.

- [ ] 8.1 Run the scripted session from 7.2 against the Dash0 dev ingress with a real auth token and a dedicated dataset; verify the wrapper's connectivity check succeeds and the session completes
- [ ] 8.2 Confirm the target dataset with `listDatasets`, then use `getSpans` filtered on `gen_ai.harness.name is opencode` and `gen_ai.conversation.id is <session id>` over the run's time range; verify the expected span set arrived and no span is missing
- [ ] 8.3 Use `getTraceDetails` on the returned trace id; verify the hierarchy matches 7.4 — chat span at the root, tool spans beneath it, `invoke_agent` with the sub-agent's own tool spans beneath that
- [ ] 8.4 Use `sql` (D0QL) to assert the token sums match the scripted usage and that the identity, VCS, and team attributes are populated; verify content attributes read `<REDACTED>` under the default `omit_io`
- [ ] 8.5 Use `getAttributeKeys` scoped to spans to diff the OpenCode attribute key set against a Claude Code session's in the same dataset; verify every key Claude Code produces for an equivalent event is either present or listed as a documented OpenCode gap in `FEATURE_MATRIX.md`
- [ ] 8.6 Rerun 8.2–8.5 with `omit_io: false` and `omit_user_info: true`; verify content attributes carry real content and `user.name` is a 16-hex-char hash with `user.email` absent
- [ ] 8.7 Document the whole recipe in `opencode/README.md` as a repeatable checklist including the filter expressions and the D0QL queries; verify by following it from scratch and recording the session id, dataset, and time range in the PR evidence

## 9. Commands and skill

- [ ] 9.1 Port `dash0-configure` to the OpenCode command format confirmed in 1.6, writing `otlp_url` and `auth_token` into the correct-scope `.local.md`; verify by running it in a scratch project and confirming a subsequent session exports
- [ ] 9.2 Port `open-session` to open the Dash0 session page for the current OpenCode session; verify the URL it produces matches the one in the startup toast and resolves to the trace found in 8.3
- [ ] 9.3 Rewrite `audit-usage` against OpenCode's message storage as confirmed in 1.6; verify its token totals for a recorded session match the totals on that session's chat spans in Dash0

## 10. Documentation

- [ ] 10.1 Add a fifth column to `FEATURE_MATRIX.md` covering runtimes, config options, config sources, transferred span properties, installation, debugging, error handling, and user notifications; verify every row has an OpenCode entry
- [ ] 10.2 Write `opencode/README.md` with local-dev instructions following `cursor/README.md`, plus the 1.3–1.6 findings and the 8.7 verification recipe; verify by following it from a clean checkout
- [ ] 10.3 Link the OpenCode guide from `DEVELOPMENT.md#per-runtime-developer-guides` and add both install paths to `README.md`; verify no other runtime's docs changed
- [ ] 10.4 Record the resolved minimum supported OpenCode version in `opencode/README.md` and the package's `peerDependencies` range; verify it matches the oldest release whose plugin types carry every field the mapping reads
- [ ] 10.5 Open a follow-up issue for the wrapper centralization described in design.md Decision 6, naming both candidate shapes and the Codex single-file delivery constraint; verify the issue links back to this change

## 11. Final verification

- [ ] 11.1 Run `make ci` and confirm lint and the full test suite pass
- [ ] 11.2 Run `make test-e2e`, `make test-live`, and `./test/contracts/run.sh` and confirm all pass
- [ ] 11.3 Run one real interactive `opencode` session against Dash0 with a real model and confirm the trace looks correct in the UI — the one check the scripted harness cannot make, since it never exercises a real model's tool-calling behavior
