## Purpose

Emits Dash0 OpenTelemetry traces for OpenCode coding sessions — LLM turns, tool
calls, and sub-agent work — so that OpenCode usage is observable on the same
dashboards, with the same span shape and attributes, as Claude Code, Cursor,
Codex, and Copilot CLI.

## ADDED Requirements

### Requirement: Span contract identical to the other runtimes

The plugin SHALL produce spans that conform to the shared span contract
documented in `DEVELOPMENT.md#telemetry-attributes` without exception: span
names (`chat <model>`, `invoke_agent <agent_type>`, `execute_tool <tool_name>`),
`SpanKind=Internal`, the resource attributes, and the on-every-span attribute
set (identity, VCS, team, conversation id, working directory).

An attribute OpenCode cannot source SHALL be omitted. The plugin SHALL NOT
introduce an OpenCode-specific attribute key for a concept the shared contract
already names, and SHALL NOT emit a zero or empty value in place of an
unavailable one.

#### Scenario: Chat span carries the shared attribute set

- **WHEN** an OpenCode assistant turn completes and telemetry is configured
- **THEN** a span named `chat <model>` is exported with `gen_ai.operation.name=chat`,
  `gen_ai.harness.name=opencode`, `gen_ai.request.model`, `gen_ai.conversation.id`
  set to the OpenCode session id, and the identity and VCS attributes the shared
  contract requires

#### Scenario: An unavailable attribute is omitted rather than zeroed

- **WHEN** OpenCode reports no value for an optional attribute in the shared contract
- **THEN** the attribute key is absent from the exported span
- **AND** no placeholder, zero, or empty-string value is emitted in its place

#### Scenario: Golden spans match the shared expectations

- **WHEN** a recorded OpenCode session's events are replayed through the plugin
- **THEN** the resulting spans match a checked-in golden file
- **AND** the golden file's span shape is verifiable against the equivalent
  Claude Code golden spans for the same logical session

### Requirement: Token usage reported per assistant turn

The plugin SHALL report OpenCode's per-message token counts on the chat span:
input, output, cache read, and cache creation tokens. When OpenCode reports a
non-zero reasoning token count, the plugin SHALL emit it as
`gen_ai.usage.reasoning.output_tokens`.

#### Scenario: Full token breakdown on a cached turn

- **WHEN** an assistant turn completes reporting input, output, cache-read and
  cache-write token counts
- **THEN** the chat span carries `gen_ai.usage.input_tokens`,
  `gen_ai.usage.output_tokens`, `gen_ai.usage.cache_read.input_tokens`, and
  `gen_ai.usage.cache_creation.input_tokens` as integers matching the reported values

#### Scenario: Reasoning tokens only when reported

- **WHEN** an assistant turn reports a reasoning token count greater than zero
- **THEN** the chat span carries `gen_ai.usage.reasoning.output_tokens`
- **WHEN** the reported reasoning token count is zero or absent
- **THEN** the attribute is omitted

### Requirement: Tool calls traced with native timings

Every completed OpenCode tool call SHALL produce one `execute_tool` span whose
duration reflects the tool's own reported start and end times, carrying the tool
name, call id, and — subject to the content-redaction rules — its arguments and
result.

A tool call that fails SHALL produce a span with status `Error` and
`exception.message` set to the reported error text.

#### Scenario: Successful tool call

- **WHEN** a tool call completes successfully
- **THEN** an `execute_tool <tool_name>` span is exported with
  `gen_ai.tool.call.id` set to the OpenCode call id and a duration equal to the
  reported end minus start time

#### Scenario: Failed tool call

- **WHEN** a tool call ends in an error state
- **THEN** the span status is `Error` and `exception.message` carries the error text

#### Scenario: Each tool call is traced exactly once

- **WHEN** OpenCode reports the same tool call more than once, including repeated
  updates for the same call id
- **THEN** exactly one `execute_tool` span is exported for that call id

### Requirement: MCP tools attributed to their real server

An MCP-provided tool call SHALL carry `dash0.gen_ai.tool.mcp_server` set to the
name of the MCP server that provided it, and `gen_ai.tool.name` SHALL be the
bare tool name with any server-qualifying prefix removed.

#### Scenario: MCP tool call

- **WHEN** a tool call provided by MCP server `dash0` completes
- **THEN** the span carries `dash0.gen_ai.tool.mcp_server=dash0`
- **AND** `gen_ai.tool.name` is the tool name without the server prefix

### Requirement: Trace correlation within a turn

All spans belonging to one user turn SHALL share a trace id allocated when the
user submits the prompt. Tool spans SHALL be children of that turn's chat span.

#### Scenario: Tool spans parent to their turn

- **WHEN** a user turn runs three tool calls before the assistant responds
- **THEN** all three `execute_tool` spans and the `chat` span share one trace id
- **AND** each `execute_tool` span's parent is the turn's `chat` span

### Requirement: Sub-agent sessions traced and parented

Work OpenCode delegates to a child session SHALL produce an `invoke_agent` span
carrying `gen_ai.agent.id` and, as `gen_ai.agent.name`, the sub-agent's type.
That span SHALL be a child of the spawning turn's chat span, and the child
session's own tool spans SHALL parent to the `invoke_agent` span.

#### Scenario: Delegated sub-agent run

- **WHEN** a turn spawns a child session that runs two tool calls and returns
- **THEN** an `invoke_agent <agent_type>` span is exported as a child of the
  spawning turn's chat span
- **AND** both child tool spans parent to that `invoke_agent` span
- **AND** all of them share the spawning turn's trace id

### Requirement: Session title reported

When OpenCode has assigned a session a title, the plugin SHALL report it as
`gen_ai.conversation.name` on the session's chat spans.

#### Scenario: Titled session

- **WHEN** OpenCode has set a title for the current session
- **THEN** chat spans for that session carry `gen_ai.conversation.name` set to it

#### Scenario: Untitled session

- **WHEN** the session has no title yet
- **THEN** `gen_ai.conversation.name` is omitted

### Requirement: Configuration keys and precedence

The plugin SHALL read the configuration keys the other runtimes support:
`otlp_url`, `auth_token`, `auth_token_keychain_service`,
`auth_token_keychain_account`, `dataset`, `agent_name`, `team_name`, `omit_io`,
`omit_user_info`, `omit_identity_fallback`, `enabled`, `debug`, `debug_file`.

Defaults SHALL match the other runtimes: `omit_io` defaults to true;
`omit_user_info` and `omit_identity_fallback` default to false.

Precedence, highest first, SHALL be: the project-scoped config file, then the
user-scoped config file, then `DASH0_*` environment variables. Config files SHALL
NOT merge across scopes — when a project file exists, the user file is ignored
entirely.

The auth token SHALL be passed to the exporting process only through the
OpenCode-prefixed secure variable and SHALL NOT be readable from a `DASH0_*`
variable.

#### Scenario: Project config wins wholesale

- **WHEN** a project config file sets only `otlp_url` and a user config file sets
  `otlp_url`, `dataset` and `team_name`
- **THEN** the project `otlp_url` is used and `dataset` and `team_name` are unset

#### Scenario: Environment fallback for a non-secret option

- **WHEN** no config file sets `dataset` and `DASH0_DATASET` is set in the environment
- **THEN** that value is used as the dataset

#### Scenario: Token is not taken from the shared namespace

- **WHEN** `DASH0_AUTH_TOKEN` is set in the environment and no config file supplies a token
- **THEN** no auth token is configured and no authenticated export is attempted

#### Scenario: Disabled by configuration

- **WHEN** the effective config sets `enabled: false`
- **THEN** no telemetry is emitted and no export is attempted

### Requirement: macOS keychain token lookup

On macOS, when `auth_token_keychain_service` is configured, the plugin SHALL read
the auth token from the named keychain item at runtime. A successful lookup SHALL
take precedence over a literal `auth_token` value.

#### Scenario: Keychain lookup succeeds

- **WHEN** `auth_token_keychain_service` names an existing keychain item and
  `auth_token` is also set
- **THEN** the token read from the keychain is used

#### Scenario: Keychain lookup fails

- **WHEN** the named keychain item does not exist and `auth_token` is set
- **THEN** the literal `auth_token` value is used

### Requirement: Telemetry never breaks the OpenCode session

No failure in the telemetry path SHALL surface as an error to the user, block a
tool call, abort a turn, or change what OpenCode does. This SHALL hold for a
missing or unsupported platform binary, a failed or corrupted download, an
unreachable OTLP endpoint, a rejected auth token, a malformed config file, and an
unrecognized event shape from a future OpenCode version.

#### Scenario: OTLP endpoint unreachable

- **WHEN** the configured endpoint refuses connections for a whole session
- **THEN** the session runs to completion normally with no user-visible error

#### Scenario: Binary unavailable for this platform

- **WHEN** no release binary exists for the host OS and architecture
- **THEN** the session runs untraced and OpenCode is otherwise unaffected

#### Scenario: Unrecognized event shape

- **WHEN** OpenCode delivers an event whose shape the plugin does not recognize
- **THEN** the event is ignored, other events continue to be traced, and no error
  reaches the user

### Requirement: Event handling does not degrade the session

The plugin SHALL forward only the events it turns into spans, SHALL do so without
blocking OpenCode's own event handling, and SHALL NOT spawn a process for events
it does not consume.

#### Scenario: High-frequency events are not forwarded

- **WHEN** OpenCode emits streaming update events for an in-progress message
- **THEN** no process is spawned for them

#### Scenario: Turn latency is unaffected

- **WHEN** telemetry is enabled and the OTLP endpoint is slow to respond
- **THEN** the user's turn completes without waiting on the export

### Requirement: Content redaction

The four content attributes — `gen_ai.input.messages`, `gen_ai.output.messages`,
`gen_ai.tool.call.arguments`, `gen_ai.tool.call.result` — SHALL be replaced with
`<REDACTED>` when `omit_io` is on, and truncated to 16 KB otherwise. Extracted
VCS references (pull request, issue, commit) SHALL survive redaction.

User identity SHALL follow `omit_user_info`: `user.name` becomes a 16-hex-char
SHA-256 hash, `user.email` is dropped, and `process.working_directory` is
home-dir-redacted.

#### Scenario: Default redaction

- **WHEN** no config sets `omit_io` and a tool call runs
- **THEN** `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` are `<REDACTED>`

#### Scenario: Content enabled and oversized

- **WHEN** `omit_io: false` and an assistant response exceeds 16 KB
- **THEN** `gen_ai.output.messages` is truncated to 16 KB

#### Scenario: Anonymized identity

- **WHEN** `omit_user_info: true`
- **THEN** `user.name` is a 16-hex-char hash, `user.email` is absent, and
  `process.working_directory` is home-dir-redacted

### Requirement: Session link shown to the user

At session start, when telemetry is configured and the endpoint is reachable, the
plugin SHALL show the user a notification containing the Dash0 session link.

#### Scenario: Connected at session start

- **WHEN** a session starts with a reachable configured endpoint
- **THEN** a notification containing the Dash0 session link for that session is
  shown in the OpenCode interface

#### Scenario: Not configured

- **WHEN** no OTLP URL is configured
- **THEN** no notification is shown

### Requirement: Two supported installation paths

The plugin SHALL be installable both as a published npm package referenced from
OpenCode's plugin configuration, and via a standalone installer script that
requires no npm registry access. Both paths SHALL yield the same telemetry
behavior. An uninstaller SHALL remove everything the installer script added.

#### Scenario: npm install path

- **WHEN** a user adds the published package to their OpenCode plugin configuration
  and starts a session
- **THEN** the plugin loads and emits telemetry per the configured settings

#### Scenario: Script install path

- **WHEN** a user runs the installer script with no npm registry access
- **THEN** the plugin is installed and a subsequent session emits the same telemetry
  as the npm path

#### Scenario: Uninstall is complete

- **WHEN** the uninstaller is run after a script install
- **THEN** the plugin file, its configuration entry, and the cached binaries are removed
- **AND** the user's own config file is left in place

### Requirement: Binary integrity on download

The plugin SHALL verify the checksum of a downloaded exporting binary against the
published checksum before executing it, and SHALL discard a binary that fails
verification rather than running it.

#### Scenario: Checksum mismatch

- **WHEN** a downloaded binary's checksum does not match the published value
- **THEN** the binary is deleted, it is never executed, and the session continues untraced

### Requirement: Debug output

When `debug` is enabled, the plugin SHALL emit diagnostic records for the
telemetry it produces, to a file when `debug_file` is set. Debug mode SHALL work
with no endpoint configured, so the pipeline can be exercised without a backend.

#### Scenario: Debug to file without a backend

- **WHEN** `debug: true` and `debug_file` are set and `otlp_url` is empty
- **THEN** diagnostic records for the spans that would have been exported are
  written to the named file and nothing is sent over the network
