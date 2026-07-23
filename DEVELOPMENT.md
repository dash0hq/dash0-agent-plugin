# Development

## Releasing

> `scripts/release.sh <version>` executes the following release steps in one go.
>
Releases are automated with [GoReleaser](https://goreleaser.com/) via GitHub Actions. To create a new release, update the version in:

- `.claude-plugin/plugin.json` — `version` field
- `.cursor-plugin/plugin.json` — `version` field
- `scripts/on-event.sh` — `VERSION=` line (Claude Code binary downloader)
- `scripts/cursor-on-event.sh` — `VERSION=` line (Cursor binary downloader)
- `scripts/codex-on-event.sh` — `VERSION=` line (Codex binary downloader)

Then tag the commit in main:

```bash
git tag v<version>
git push --tags
```

This triggers the release workflow which cross-compiles binaries for `darwin/linux × amd64/arm64` and publishes them to [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases).
The `on-event-<agent>.sh` scripts download the matching binaries on first run.

## Feature support matrix

See **[FEATURE_MATRIX.md](./FEATURE_MATRIX.md)** for the full per-runtime comparison
across configuration options, transferred span  properties, installation, debugging,
error handling, and user notifications.

## Per-runtime developer guides

Building, sideloading, and running local changes is documented per runtime:

- **Claude Code** — [claude/README.md](./claude/README.md)
- **Cursor** — [cursor/README.md](./cursor/README.md)
- **OpenAI Codex** — [codex/README.md](./codex/README.md)

## Telemetry attributes

Spans follow [GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/).

Identity, VCS, and team attributes go on **every** span, the rest depend on the span type.
Values are strings unless noted as integers.

> The four content attributes `gen_ai.input.messages`, `gen_ai.output.messages`,
> `gen_ai.tool.call.arguments`, `gen_ai.tool.call.result` are replaced with `<REDACTED>`
> when `omit_io` is on (the default) and truncated to  16 KB otherwise.

> The three user-identity attributes behave according to `omit_user_info` (off by default):
> `user.name` becomes a 16-hex-char SHA-256 hash, `user.email` is
dropped entirely, and `process.working_directory` is home-dir-redacted to `~`.

### Span shape

| Field | Value |
|---|---|
| Span name | `chat <model>`, `invoke_agent <agent_type>` (sub-agent), or `execute_tool <tool_name>` |
| Span kind | `Internal` (always) |
| Status | `Unset` normally; `Error` (with `exception.message`) on `StopFailure` / `PostToolUseFailure` |
| Trace / parent IDs | random per turn, allocated at prompt submit; tool spans and sub-agents parent to the turn's chat span |

### Resource attributes

| Key | Value |
|---|---|
| `service.name` | Agent name — `claude-code` / `cursor` / `codex`, or the `agent_name` override |
| `service.version` | Plugin version (`dev` in source runs) |

### On every span

| Key | Value / Example | Notes |
|---|---|---|
| `gen_ai.provider.name` | `anthropic`, `openai`, `gcp.gemini`, `x_ai`, `deepseek`, `mistral_ai`, `cursor` | Resolved from the model prefix; else the runtime default (Claude `anthropic`, Codex `openai`; Cursor omits it when no model). |
| `gen_ai.agent.name` | Agent name, or the sub-agent type on `invoke_agent` spans                      | |
| `gen_ai.harness.name` | `claude-code` / `cursor` / `codex`                                             | |
| `dash0.team.name` | e.g. `platform`                                                                | Only when `team_name` is set. |
| `gen_ai.conversation.id` | Session ID                                                                     | From the event's `session_id`. |
| `process.working_directory` | e.g. `/home/me/proj`                                                           | `~`-redacted when `omit_user_info`. |
| `dash0.gen_ai.vcs.repository.url.full` | `https://github.com/dash0hq/dash0-agent-plugin`                                | git remote, normalized to https. |
| `dash0.gen_ai.vcs.repository.name` | `dash0-agent-plugin`                                                           | |
| `dash0.gen_ai.vcs.owner.name` | `dash0hq`                                                                      | |
| `dash0.gen_ai.vcs.provider.name` | `github` / `gitlab` / `bitbucket` / `gitea`                                    | From the remote host. |
| `dash0.gen_ai.vcs.ref.head.name` | e.g. `main`                                                                    | Branch or tag name. |
| `dash0.gen_ai.vcs.ref.head.revision` | commit SHA                                                                     | |
| `dash0.gen_ai.vcs.ref.head.type` | `branch` or `tag`                                                              | |
| `user.name` | Real name, or a 16-hex-char SHA-256 hash when `omit_user_info`                 | |
| `user.email` | git email                                                                      | Omitted when `omit_user_info`. |

The VCS and `user.*` keys are only present inside a git repository; any individual key is omitted when its value is empty.

### LLM / chat spans (`chat` and `invoke_agent`)

| Key | Value / Example | Notes                          |
|---|---|--------------------------------|
| `gen_ai.operation.name` | `chat` or `invoke_agent`                                             |                                |
| `gen_ai.request.model` | `claude-…`, `gpt-…`, `cursor-auto`, …                                |                                |
| `gen_ai.conversation.name` | Session title                                                        | Claude only (from transcript). |
| `gen_ai.usage.input_tokens` | integer                                                              |                                |
| `gen_ai.usage.output_tokens` | integer                                                              |                                |
| `gen_ai.usage.cache_read.input_tokens` | integer                                                              |                                |
| `gen_ai.usage.cache_creation.input_tokens` | integer                                                              | Not emitted by Codex.          |
| `gen_ai.input.messages` | JSON: `[{"role":"user","parts":[{"type":"text","content":"…"}]}]`    | Content-gated by `omit_io`.    |
| `gen_ai.output.messages` | JSON: `[{"role":"assistant","parts":[{"type":"text","content":"…"}]}]` | Content-gated by `omit_io`.    |
| `gen_ai.agent.id` | Sub-agent ID                                                         | On`invoke_agent` spans.        |
| `exception.message` | Error text                                                           | On `StopFailure`.              |

### Tool-call spans (`execute_tool`)

| Key | Value / example | Notes |
|---|---|---|
| `gen_ai.operation.name` | `execute_tool` | |
| `gen_ai.tool.type` | `function` | Constant. |
| `gen_ai.tool.name` | `Bash`, `Read`, … | MCP tool names are stripped of their `mcp__<server>__` prefix; the server goes to `dash0.gen_ai.tool.mcp_server`. |
| `gen_ai.tool.call.id` | Tool-use ID | |
| `gen_ai.tool.call.arguments` | Tool input (JSON / string) | Content-gated, ≤16 KB. |
| `gen_ai.tool.call.result` | Tool output | Content-gated, ≤16 KB. |
| `dash0.gen_ai.tool.mcp_server` | MCP server name (placeholder `cursor` on Cursor) | MCP tools only. |
| `dash0.gen_ai.tool.bash.command_family` | Binary name, e.g. `git`, `npm` | Bash tool. |
| `dash0.gen_ai.tool.skill.name` | Skill name | Skill tool. |
| `dash0.gen_ai.code.lines_added` | integer | Claude Code only — from the Edit/Write/MultiEdit `structuredPatch`. |
| `dash0.gen_ai.code.lines_removed` | integer | Claude Code only — from the Edit/Write/MultiEdit `structuredPatch`. |
| `dash0.gen_ai.vcs.pull_request.url` | PR / MR URL | Survives `omit_io`. |
| `dash0.gen_ai.vcs.issue.url` | Issue URL | Survives `omit_io`. |
| `dash0.gen_ai.vcs.commit.sha` | Commit SHA | Survives `omit_io`. |
| `exception.message` | Error text | On `PostToolUseFailure`. |
