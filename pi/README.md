# Pi

This [Pi extension](https://pi.dev) sends each completed Pi turn to Dash0. A
turn has its model calls and its tool calls. The extension runs inside Pi and
does not wrap the `pi` command.

## How it works

`index.ts` listens to Pi's extension events. It collects one user turn and
gives it to `pi-on-event`, a Go helper next to `index.ts`. The helper builds the
spans with the shared `internal/otlp` builders and sends them. This is the same
design as the [Amp plugin](../amp/README.md): there is no hook, no bootstrap
and no state on disk between turns.

| Pi event                          | Use                                                                          |
| --------------------------------- | ---------------------------------------------------------------------------- |
| `before_agent_start`              | Starts a turn. Records the prompt of a user run.                             |
| `agent_start`                     | Starts a turn for a run that no prompt started.                              |
| `message_end` (assistant)         | One model call: provider, model, timing, stop reason, effort level, tokens.  |
| `tool_execution_start` and `_end` | One tool call: timing, error flag, arguments, result.                        |
| `agent_settled`                   | Ends the turn and sends it. `aborted` marks a turn that the user stopped.    |
| `session_shutdown`                | Drops an open turn and waits up to 2 seconds for envelopes still in transit. |

A turn ends at `agent_settled`, not at `agent_end`. After `agent_end`, Pi can
retry a model call, compact the context and retry, or run queued follow-up
messages. All of that is one user turn.

The extension starts the helper detached and does not wait for it. A turn
never waits for an export. When the export fails, the extension shows
`dash0: telemetry unavailable` one time per session.

## Install from source

Releases build `pi-on-event-<os>-<arch>` from the next version on. There is no
installer or Pi package yet. Until then, build the helper next to `index.ts`:

```sh
go build -o pi/pi-on-event ./cmd/pi-on-event
```

Then load the extension in one of these ways:

- For one run: `pi -e /path/to/dash0-agent-plugin/pi/index.ts`
- For all runs: add the `pi` directory to `extensions` in the `settings.json`
  of your Pi agent directory, or run `pi install /path/to/dash0-agent-plugin/pi`.
  The `pi` field in `package.json` names `index.ts` as the entry point.

`index.ts` looks for `pi-on-event-<os>-<arch>` first, then for `pi-on-event`.
On Windows, both names end in `.exe`.

## Configure

The helper reads `dash0-agent-plugin.local.md`, as every other runtime does.
The first file that exists wins as a whole. Values are not merged.

1. `.pi/dash0-agent-plugin.local.md` in the project directory.
2. `~/.pi/dash0-agent-plugin.local.md` in the home directory.

The second file is not in Pi's agent directory (`~/.pi/agent`), and
`PI_CODING_AGENT_DIR` does not move it. Every Pi profile on a machine thus
reads the same file. For a value per profile, set `PI_PLUGIN_OPTION_*` in the
profile's launcher.

```markdown
---
otlp_url: https://ingress.eu-west-1.aws.dash0.com
auth_token: auth_xxx
dataset: default
---
```

Environment variables override the file, with the `PI_PLUGIN_OPTION_` prefix:
`PI_PLUGIN_OPTION_OTLP_URL`, `PI_PLUGIN_OPTION_AUTH_TOKEN`, and so on. Ordinary
options also fall back to `DASH0_<KEY>`. The token never does. See the
[feature matrix](../FEATURE_MATRIX.md) for the shared options.

`omit_io` is `true` by default. Pi tool arguments and results can hold whole
files, so keep it on unless you need the content.

## Telemetry

| Span                  | Parent                           | Contents                                                                                               |
| --------------------- | -------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `chat <model>`        | none                             | The turn. The last model call names it and supplies its tokens.                                        |
| `chat <model>`        | turn                             | Each earlier model call, with its own timing, model, provider and tokens.                              |
| `execute_tool <tool>` | turn, or the tool that called it | One tool call. A nested call, such as one a `codemode` script makes, is a child of that script's span. |

`gen_ai.harness.name` is `pi`, and the default `service.name` is `pi`.

One Pi session can use several providers. Every span therefore takes
`gen_ai.provider.name` from its own model call, never from the session. A tool
span names the model and provider of the call that requested it. Pi's provider
ID is also on every chat span as `dash0.pi.provider`. Pi provider IDs map to
the GenAI provider names. For example, `claude-code` and `anthropic` map to
`anthropic`, and `openai-codex` maps to `openai`. The helper exports an
unknown ID unchanged.

Pi reports input tokens without cache reads and cache writes, for every
provider. The helper adds them, so `gen_ai.usage.input_tokens` includes the
cache, as the GenAI conventions define it. Dash0's cost calculation expects
this from every harness except Claude Code. A model call that reports no
tokens at all, for example one aborted before the response, has no token
attributes.

| Attribute                                 | Value                                                                                                                                                                                         |
| ----------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `dash0.gen_ai.tool.skill.name`, `.source` | The skill a turn loaded. `command` on the turn root for a `/skill:name` prompt. `model` on the `read` span that read a loaded skill's `SKILL.md`.                                             |
| `dash0.pi.provider`                       | Pi's provider ID, for example `claude-code`. On chat spans.                                                                                                                                   |
| `dash0.gen_ai.billing_mode`               | `subscription` when Pi used a subscription login for the call, for example Claude Pro/Max through `claude-code` or ChatGPT through `openai-codex`. Else `unknown`. On chat spans with tokens. |
| `dash0.pi.session.parent.id`              | The session that Pi forked or cloned this session from. On every span.                                                                                                                        |
| `dash0.pi.truncated`                      | `true` when the extension dropped content to stay inside its 1 MiB envelope. Absent otherwise.                                                                                                |

## Claude Code through Pi

Pi's `claude-code` provider runs Claude Code as a worker process. The worker
starts with `--setting-sources ""`, `--no-session-persistence` and
`DISABLE_TELEMETRY=1`, so it does not load the Dash0 Claude Code plugin. A Pi
turn on that provider is thus reported one time, by this extension, with
`dash0.pi.provider=claude-code`. A local run on 2026-11-13 confirmed this:
four turns on `claude-code` created no new Claude plugin session directory.

If a future version of the `claude-code` provider loads user settings, the
Claude Code plugin can also report the same model calls under
`gen_ai.harness.name=claude-code`. Check for this before you change the
provider's flags.

## Sub-agents

Pi has no built-in sub-agent. The [pi-subagents](https://github.com/nicobailon/pi-subagents)
extension adds a `subagent` tool. Its children run in one of two ways, and
each reaches Dash0 differently. Measured with pi-subagents 0.88.0:

| Child                      | Where it runs                                                                                                             | What Dash0 shows                                                                                                                                                |
| -------------------------- | ------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Foreground (the default)   | A session inside the parent's Pi process. It loads none of the parent's extensions, so this extension does not run in it. | Under the parent's `execute_tool subagent` span: one `invoke_agent <agent>` span per child, and under it one `chat <model>` span with the child's total tokens. |
| Background (`async: true`) | A session in a detached runner process. It loads the parent's extensions.                                                 | A separate session with its own traces. `dash0.pi.session.parent.id` names the parent session.                                                                  |

The foreground totals come from the `subagent` tool result. The `chat` span
under `invoke_agent` thus adds all model calls of the child, and its time
range is the whole child run. `dash0.pi.subagent.timing=aggregate` marks it,
and `dash0.pi.subagent.turns` gives the number of calls. The span is a
`chat` span because Dash0's token and cost metrics read only `chat` spans.

For per-call detail of a foreground child, load this extension into
children too, with the pi-subagents setting in Pi's `settings.json`:

```json
{
  "subagents": {
    "defaultSubagentOnlyExtensions": ["/path/to/dash0-agent-plugin/pi"]
  }
}
```

The child then exports its own session, as a background child does. The
parent still sends the `invoke_agent` span, but without a model or tokens, so
the child's tokens are counted one time.

## Limits

- Sub-agents that run as separate `pi` processes, as in Pi's `subagent`
  example, are separate sessions with no link to the parent.
- A background pi-subagents child is not inside the parent's trace. The
  `subagent` call returns before the child starts work, and pi-subagents does
  not give the child the parent's tool call.
- A model call's timing comes from Pi's own `timestamp` and `durationMs`.
  The extension measures tool timing from `tool_execution_start` to
  `tool_execution_end`. This includes time in other extensions' `tool_call`
  handlers, such as an approval prompt.
- The extension sends the last assistant text of the turn as the response.
  Thinking blocks and image data are never sent.
- `pi --mode json`, `--mode rpc` and print mode load the extension like the
  interactive mode does.
- When Pi exits during a turn, that turn is lost.
- A forked session links to its parent only when Pi saved the parent to a
  session file. A session started with `--no-session` has no file, so its
  forks have no `dash0.pi.session.parent.id`.

## Test

```sh
make test-pi      # bridge tests, type check, formatting; requires Bun
go test ./internal/source/pi/ ./cmd/pi-on-event/ ./internal/harness/
```

`transport.test.ts` builds the helper and runs it against a local collector.
