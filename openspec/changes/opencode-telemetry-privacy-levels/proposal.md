## Why

Today the whole content-privacy question is one boolean, `omit_io`, and it
answers for everything at once: prompt text, assistant response text, tool
arguments and tool results all go out together or none of them do. That forces a
choice nobody wants to make. A team that needs to see *which* tools an agent
reaches for — to spot a `gh` loop, an expensive `tools invoke`, a runaway
`curl` — has to also ship every prompt the user typed. A team that will not ship
prompt text loses tool visibility as collateral, and with it most of the reason
to instrument the agent at all.

The three things have genuinely different sensitivities. A tool *name* is close
to harmless and highly diagnostic; a tool *argument* can be a customer
identifier, a PromQL query, or a URL with a token in it. A skill name is a label
from a fixed vocabulary; a prompt is free text the user typed. One switch cannot
express that, so it gets set to the most restrictive value any stakeholder needs
and the telemetry is worth less to everyone.

## What Changes

- Replace the single `omit_io` boolean with **four independent privacy
  dimensions** — `prompts`, `tools`, `skills`, `agents` — each set to one of
  three levels: `disabled`, `limited`, `full`.
- **`tools`** levels are defined against the OpenTelemetry `execute_tool` span's
  own requirement levels, so `limited` is the semconv-default posture rather
  than a Dash0 invention:
  - `disabled` — no `execute_tool` span is emitted.
  - `limited` — Required, Conditionally Required and Recommended attributes
    only (`gen_ai.tool.name`, `gen_ai.tool.type`, `gen_ai.tool.call.id`,
    `error.type`) plus the derived Dash0 attributes. The Opt-In
    `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result` are omitted.
  - `full` — everything, including the Opt-In arguments and result.
- **New: a CLI subcommand depth table.** At `tools: limited` a bash call reports
  its *command shape* — the binary and its subcommand path — with every operand
  and flag value redacted. `gh repo clone https://…` becomes `gh repo clone`;
  `tools invoke dash0.getLogRecords --args='{…}'` becomes
  `tools invoke dash0.getLogRecords`. This is what makes `limited` genuinely
  useful instead of merely safe.

  Stopping at the first dash is not enough on its own, because a positional
  operand never reaches a dash: `gh repo clone <url>`, `cat <path>` and
  `rg <pattern>` would each be reported verbatim. So the shape is bounded two
  ways at once — a per-binary **subcommand depth** (how many leading tokens are
  subcommand rather than operand), *and* an early stop at the first flag or
  shell metacharacter. A binary that is not in the table has depth 0 and reports
  its name alone, so an unrecognized CLI fails closed rather than emitting an
  operand as if it were a subcommand.

  The table is a small list of depths, not a grammar of commands and their
  flags. It covers, at minimum, every CLI in the `agents-worker` sandbox image
  and the `tools` CLI the Dash0 agent driver prompt documents.
- **Widen `bash_command_family`** from the leading binary only (`gh`) to the
  binary plus its subcommand path (`gh pr create`), which is the attribute the
  shape library populates.
- **`prompts`** levels: `disabled` omits `gen_ai.input.messages` /
  `gen_ai.output.messages` entirely; `limited` keeps today's JSON envelope with
  `<REDACTED>` content **and adds a character-count attribute** so prompt-size
  distribution stays visible without content; `full` sends the text, still
  capped at 16 KB.
- **`skills`** governs `Skill` tool calls and **overrides** the `tools` level for
  them, so `tools: disabled, skills: limited` still reports which skills ran.
- **`agents`** governs sub-agent delegation — `invoke_agent` spans and
  `gen_ai.agent.name` — independently of the other three.
- **Not breaking.** `omit_io` keeps working and keeps its current default.
  `omit_io: true` maps to `prompts: limited, tools: limited`; `omit_io: false`
  maps to `full` for both. An explicit dimension wins over `omit_io`.
- **Scope: the OpenCode plugin only.** The four dimensions are configurable in
  OpenCode's config file and documented in its README. The other four runtimes
  keep exactly the behaviour they have today.

## Capabilities

### New Capabilities

- `opencode-plugin/telemetry-privacy`: The four privacy dimensions, their three
  levels, the attribute set each level admits, the precedence rules between
  dimensions and against the legacy `omit_io`, and the CLI command shape library
  that makes the `limited` tool level informative.

### Modified Capabilities

None. `opencode-plugin` is still an unarchived change, so its spec is not yet
under `openspec/specs/`. Its "Tool calls traced with native timings" requirement
already defers argument and result reporting to "the content-redaction rules";
this change is what those rules become, so it refines that requirement without
contradicting it. The two specs are reconciled when both are archived.

## Impact

**Shared code, OpenCode-only behaviour change.** The config plumbing lives in
packages all five runtimes share, so the work is done there once and only
OpenCode's configuration surface exposes it. Every other runtime resolves the
same defaults it resolves today.

- `internal/otlp` — `Config` gains the four levels; `eventAttributes`'
  content-redaction branch is driven by level rather than by the `OmitIO`
  boolean. `NewToolSpan` learns to be suppressed entirely at `tools: disabled`.
- `internal/harness` — `Config()` resolves the four dimensions, including the
  `omit_io` back-compatibility mapping.
- `internal/pipeline` — `ExtractBashCommandFamily` widens to a subcommand path;
  a new subcommand depth table backs it. `EnrichToolEvent` gains the redaction of
  operands and flag values. The tool-span send path learns to drop a span.
- `internal/source/opencode` — unchanged. Normalization is level-independent by
  design; redaction happens downstream in the OTLP layer.
- `opencode/opencode-on-event.sh` — parses the four new config keys. Note main
  has since landed a Go-side config reader (`internal/config`), so this may
  instead be handled there via `harness.OpenCode.ConfigDir`.
- `opencode/README.md`, `FEATURE_MATRIX.md`, `DEVELOPMENT.md` — document the
  four dimensions and mark them OpenCode-only.
- **No new dependency.** The depth table is a checked-in Go map, not a
  third-party shell parser.

**Risk.** The depth table is the one part that can leak by being wrong. A CLI
absent from the table must fail closed — report the binary alone and redact the
rest — rather than guessing that the second token is a subcommand and emitting
an operand as if it were one. The table is therefore an allowlist, never a
heuristic with an allowlist of exceptions.
