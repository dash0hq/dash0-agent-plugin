# Feature support matrix across coding agents

Three runtimes ship today — **Claude Code**, **Cursor**, and **OpenAI Codex** — on
one shared Go pipeline (`cmd/*/main.go` → `internal/pipeline` → `internal/otlp`).
They differ in how they're installed, how config reaches the hook, what the host
exposes to a hook, and (consequently) which span properties can be populated.

## Runtimes at a glance

| | Claude Code | Cursor | Codex |
|---|---|---|---|
| `gen_ai.harness.name` | `claude-code` | `cursor` | `codex` |
| `gen_ai.provider.name` | `anthropic` fallback + per-model | per-model only | `openai` fallback + per-model |
| Default `service.name` / agent name | `claude-code` | `cursor` | `codex` |
| Entrypoint | `cmd/on-event` | `cmd/cursor-on-event` | `cmd/codex-on-event` |
| Config file | `~/.claude/dash0-agent-plugin.local.md` (or `.claude/…`) | `~/.cursor/dash0-agent-plugin.local.md` (or `.cursor/…`) | `~/.codex/dash0-agent-plugin.local.md` (or `.codex/…`) |
| Per-session state dir | `$CLAUDE_PLUGIN_DATA` (required) | `~/.local/state/dash0-agent-plugin/cursor` | `~/.local/state/dash0-agent-plugin/codex` |
| Hooks registered in | plugin manifest `hooks/hooks.json` | `~/.cursor/hooks.json` (merged) | `~/.codex/config.toml` (managed block) |
| Wired hook events | 24 | 9 | 10 |

## Configuration options

Frontmatter keys in the `.local.md` file. The shell wrapper (`scripts/*-on-event.sh`)
parses them and exports the env vars the binary reads. "No (env only)" means the
wrapper doesn't parse that key from the file, so it must be set as an environment
variable instead.

| Option (file key) | Claude Code | Cursor | Codex | Notes |
|---|---|---|---|---|
| `otlp_url` | Yes | Yes | Yes | Dash0 OTLP ingress. Empty ⇒ telemetry off. |
| `auth_token` | Yes | Yes | Yes | Secure var only, no `DASH0_*` fallback: `{CLAUDE,CURSOR,CODEX}_PLUGIN_OPTION_AUTH_TOKEN`. |
| `dataset` | Yes | Yes | Yes | `Dash0-Dataset` header. |
| `agent_name` | Yes | Yes | Yes | → `service.name` / `gen_ai.agent.name`. |
| `team_name` | Yes | Yes | Yes | → `dash0.team.name`. |
| `omit_io` | Yes | Yes | Yes | Binary default `true` (redact prompts + tool I/O).¹ |
| `omit_user_info` | Yes | Yes | Yes | Default `false`. |
| `enabled` | Yes | Yes | Yes | `false` ⇒ wrapper exits, plugin off for that scope. |
| `debug` | No (env only) | Yes | Yes | Claude: use `DASH0_DEBUG`. |
| `debug_file` | No (env only) | Yes | Yes | Claude: use `DASH0_DEBUG_FILE`. |
| `show_session_link` | Yes (plugin option / env) | No | No | Claude-only feature; not parsed from `.local.md` — set via `/plugin → Configure` or `DASH0_SHOW_SESSION_LINK`. Cursor/Codex binaries don't consume it. |

¹ The Cursor and Codex README example configs show `omit_io: false`, but the installers
don't write the key. With no explicit setting the binary default (`true`) applies on all
three runtimes.

## Configuration sources & precedence

| | Claude Code | Cursor | Codex |
|---|---|---|---|
| Plugin UI (`/plugin → Configure`) | Yes (token → OS keychain) | No | No |
| `pluginConfigs` in `settings.json` | Yes (user + project) | No | No |
| `.local.md` config file | Yes (project > user) | Yes (project > user) | Yes (project > user) |
| `DASH0_*` env fallback (non-secret) | Yes (after `CLAUDE_PLUGIN_OPTION_*`) | Yes | Yes |

Precedence, highest wins:

- **Claude Code:** `settings.json` (project → user) → `.local.md` (project → user) → `DASH0_*`
- **Cursor / Codex:** `.local.md` (project → user) → `DASH0_*`

Config files never merge across scopes: if a project file exists, the user file is ignored entirely.

## Transferred span properties

Two span types are produced — `chat <model>` / `invoke_agent <type>` and
`execute_tool <name>` — all `SpanKind=Internal`. Logs, metrics, and a standalone
`session_start` span exist in code but are not wired for real sessions (only the
demo generator uses them).

| Property / capability | Claude Code | Cursor | Codex | Notes |
|---|---|---|---|---|
| Chat (LLM) span | Yes | Yes | Yes | |
| Tool-call span | Yes | Yes | Yes | |
| `gen_ai.request.model` | Yes | Yes (`default`→`cursor-auto`) | Yes | |
| Input / output tokens | Yes | Yes | Yes | |
| `cache_read.input_tokens` | Yes | Yes | Yes | |
| `cache_creation.input_tokens` | Yes | Yes | No | Codex doesn't report it. |
| Reasoning tokens | No | No | No | Codex parses but doesn't emit. |
| Sub-agent `invoke_agent` span + parenting | Yes | Partial | Yes | Cursor: `subagentStart` dropped; the stop span dangles under the chat span. |
| MCP server attribute (`dash0.gen_ai.tool.mcp_server`) | Yes (real server) | Partial (placeholder `cursor`) | Yes (real server) | |
| Tool-call duration | Native | Native | Reconstructed from `PreToolUse` | |
| Session title (`gen_ai.conversation.name`) | Yes | No | No | Only Claude has a transcript reader. |
| Prompt / response content (`gen_ai.input/output.messages`) | Yes | Yes | Yes | Gated by `omit_io`, truncated at 16 KB. |
| VCS + code enrichment (repo / branch / PR / issue / commit / lines / bash-family / skill) | Yes | Yes | Yes | Shared pipeline extractors. |
| Usage source | Claude JSONL transcript | `afterAgentResponse` hook | Codex rollout file | |

## Installation options

| | Claude Code | Cursor | Codex |
|---|---|---|---|
| Marketplace | `/plugin install dash0@…` | No (local-plugin dir scan) | `codex plugin add dash0-agent-plugin@dash0` |
| `curl \| bash` installer | No | `install-cursor.sh` | `install-codex.sh` |
| Uninstaller | via `/plugin` | `uninstall-cursor.sh` | `uninstall-codex.sh` |
| Local dev | `claude --plugin-dir …` ([guide](claude/README.md)) | symlink into `~/.cursor/plugins/local/` ([guide](cursor/README.md)) | `emit-codex-hooks` ([guide](codex/README.md#build--run-locally)) |
| Binary delivery | download + checksum (`on-event.sh`) | download + checksum (`cursor-on-event.sh`) | download + checksum (`codex-on-event.sh`) |
| Hook trust step | None | None | Yes — reproduced trust-hash in `config.toml` (installer) or manual `/hooks` (marketplace path) |
| Extra requirement | — | `jq` | `jq` |

## Debugging

| | Claude Code | Cursor | Codex |
|---|---|---|---|
| Enable via config file | No | `debug` / `debug_file` | `debug` / `debug_file` |
| Enable via env | `DASH0_DEBUG` / `DASH0_DEBUG_FILE` | same | same |
| Output | `[dash0:trace\|log\|metric]` to stderr and/or file | same | same |
| Runs pipeline without a backend (empty `otlp_url`) | Yes (when debug on) | Yes | Yes |
| Primary path | env vars | config file | config file |

## Error handling

Shared principle: **telemetry never breaks the agent loop.** `pipeline.Process`
swallows export errors; each hook sends synchronously with a 5s timeout, 2 attempts,
and a 500ms retry delay.

| | Claude Code | Cursor | Codex |
|---|---|---|---|
| Wrapper on failure | `set -euo pipefail`; may `exit 1` on download/checksum error | fail-open, `exit 0` | fail-open, `exit 0` |
| Binary on `run()` error | logs stderr, `exit 1` | logs stderr, `exit 0` | logs stderr, `exit 0` |
| Rationale | Claude tolerates a non-zero observational-hook exit | Cursor blocks on non-zero when `failClosed` | Codex may block on non-zero |
| Connectivity check (SessionStart) | Yes | Yes | Yes |
| Missing `session_id` | random ID + `dash0.warning` | same | same |

## User notifications

The pipeline produces status messages (e.g. the `dash0: connected → <session
link>` welcome banner) uniformly, but only **Claude Code** can show them to the
user at session start. The others expose only a model-context field there, or a
diagnostic log the user doesn't normally see — so the banner renders on Claude
Code alone.

| Agent | User-visible message | Model-context injection | Notes |
|-------|----------------------|-------------------------|-------|
| Claude Code | `systemMessage` (any hook) | `additionalContext` | Full support. |
| Cursor | `user_message` — only when a hook **denies** an action | `additional_context` (sessionStart) | No unblocked startup banner. [docs](https://cursor.com/docs/hooks.md) |
| Codex | none (hook stderr not surfaced; `notify` is OS-only) | none | Nothing user-visible. [docs](https://learn.chatgpt.com/docs/config-file/config-reference) |
| Copilot CLI | none at sessionStart (stderr only on exit 2) | `additionalContext` (sessionStart) | Open bug [copilot-cli#1352](https://github.com/github/copilot-cli/issues/1352). [docs](https://docs.github.com/en/copilot/reference/hooks-reference) |

For the three non-Claude agents, injecting the session link as model context is
the only portable fallback — it lets the agent surface the link if asked, but
does not display it directly.
