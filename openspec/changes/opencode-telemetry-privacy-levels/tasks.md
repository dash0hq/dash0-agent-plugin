## 1. The level type and its resolution

- [ ] 1.1 Add `otlp.Level` (`LevelDisabled`, `LevelLimited`, `LevelFull`) with a
  parser whose zero value and unparseable input both yield `LevelLimited`, and
  verify a table test covers each valid spelling, the empty string, and a typo
- [ ] 1.2 Add `Prompts`, `Tools`, `Skills`, `Agents` to `otlp.Config` and verify
  `go build ./...` passes with every entrypoint still compiling
- [ ] 1.3 Implement the resolution helper in `internal/harness` encoding the full
  precedence chain (explicit dimension → `omit_io` mapping → default), and verify
  a test covers all three `omit_io` scenarios from the spec: untouched config,
  explicit dimension overriding `omit_io`, and `omit_io: false` with no dimension
- [ ] 1.4 Wire the helper into `harness.Config()` and verify the existing
  `internal/harness` suite still passes unchanged

## 2. Prompt levels

- [ ] 2.1 Convert `contentKeys` from `map[string]bool` to a key → dimension map
  and switch `eventAttributes` on the governing dimension's level, and verify the
  existing `internal/otlp` redaction tests pass with no assertion edits
- [ ] 2.2 Implement `disabled` for prompts — attribute omitted rather than
  placeholder — and verify a test asserts neither `gen_ai.input.messages` nor
  `gen_ai.output.messages` is present
- [ ] 2.3 Add the withheld-content character count at `limited`, settling the
  `dash0.`-prefixed attribute name from design.md's Open Questions, and verify a
  test asserts the count matches the original content length
- [ ] 2.4 Verify a test replays one turn at all three prompt levels and asserts
  the model, provider, token count, conversation id, duration and status
  attributes are identical across the three chat spans

## 3. The CLI subcommand depth table

- [ ] 3.1 Add the depth table as a checked-in map covering every CLI the spec
  names (the `agents-worker` image set plus `tools`), and verify a test asserts
  each listed binary is present with a documented depth
- [ ] 3.2 Rewrite `ExtractBashCommandFamily` to emit the command shape — skip
  leading `KEY=value` assignments, take the binary, then up to `depth` further
  tokens, stopping at the first flag or shell metacharacter — and verify a table
  test covers every scenario in the spec's bash requirement
- [ ] 3.3 Verify a test asserts the unknown-binary path reports the binary alone
  and that neither a subcommand nor an operand of an unlisted CLI is ever emitted
- [ ] 3.4 Verify a test asserts the shape never contains a token beginning with
  `-`, a shell metacharacter, a `/`, or a `=`, for every command in a fixture
  list of realistic sensitive invocations
- [ ] 3.5 Update the callers and golden expectations of `bash_command_family`
  across the runtimes for the widened value, and verify every runtime's golden
  span suite passes

## 4. Tool and skill levels

- [ ] 4.1 Gate `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` on the
  `tools` level so `limited` omits both, and verify a test asserts the span still
  carries name, type, call id, start time, end time and status
- [ ] 4.2 Suppress the `execute_tool` span entirely at `tools: disabled` by
  returning early in `pipeline.Process` before `sendToolTrace`, and verify a test
  asserts no tool span is exported while the turn's chat span still is
- [ ] 4.3 Confirm `EnrichToolEvent` runs on the full `tool_input` before any
  level gating, and verify a test asserts `bash_command_family`, `skill_name` and
  `mcp_server` are present at `limited`
- [ ] 4.4 Suppress the failure message at `tools: limited` while keeping the
  `Error` status and error type, and verify a test asserts the message text is
  absent from every attribute
- [ ] 4.5 Route `Skill` tool calls through the `skills` dimension so it overrides
  `tools` for them, and verify tests cover both spec scenarios — skills visible
  with `tools: disabled`, and skills suppressed with `tools: full`

## 5. Sub-agent level

- [ ] 5.1 Suppress the `invoke_agent` span at `agents: disabled` and write the
  delegating turn's span id into the per-agent trace-context snapshot in place of
  the suppressed span's, and verify a test asserts a child tool span parents to
  the chat span
- [ ] 5.2 Verify a test replays a full delegated session at `agents: disabled`
  and asserts no exported span references a parent span id that was never
  exported
- [ ] 5.3 Gate the sub-agent's prompt and response content on the `agents` level
  and verify a test asserts `limited` carries the agent name with no content

## 6. Configuration surface

- [ ] 6.1 Settle design.md's Open Question on where the four keys are read —
  `opencode-on-event.sh` or `internal/config` via `OpenCode.ConfigDir` — then
  implement it and verify a config file setting all four dimensions produces the
  expected resolved levels
- [ ] 6.2 Verify `test/contracts/opencode.sh` covers a config file setting the
  four dimensions and asserts the resulting spans against the mock OTLP server
- [ ] 6.3 Verify a test asserts that setting the four dimensions changes no
  attribute on any span exported by the Claude, Cursor, Codex or Copilot
  entrypoints

## 7. Leak guards

- [ ] 7.1 Add a test asserting every key in `contentKeys` maps to a dimension, so
  a future content key cannot be added ungoverned
- [ ] 7.2 Add a test asserting debug output at `prompts: disabled, tools: limited`
  contains no prompt text, response text, tool arguments or tool results
- [ ] 7.3 Add an end-to-end leak test that replays a session whose prompts, tool
  arguments and tool results all contain a unique sentinel string, at each
  restrictive level, and asserts the sentinel appears nowhere in the captured
  OTLP payloads or debug output

## 8. Documentation

- [ ] 8.1 Document the four dimensions, their levels, the precedence rules and
  the depth table in `opencode/README.md`, and verify the config example there
  parses under the reader implemented in 6.1
- [ ] 8.2 Update `FEATURE_MATRIX.md` to show the four dimensions as OpenCode-only
  and `omit_io` as still supported everywhere, and verify the table's claims match
  the tests in sections 2 through 5
- [ ] 8.3 Update `DEVELOPMENT.md`'s telemetry-attributes table to state which
  dimension gates each content attribute, replacing the `omit_io` references
