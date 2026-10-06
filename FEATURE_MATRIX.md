# Feature support matrix across coding agents

Six runtimes are implemented — **Claude Code**, **Cursor**, **OpenAI Codex**,
**GitHub Copilot CLI**, **OpenCode V2**, and the **GitHub Copilot app** — sharing Go configuration and OTLP
export (`internal/harness`, `internal/otlp`). The hook adapters and the
Copilot app adapter use `internal/pipeline`; OpenCode's stream consumer uses its tool enrichment helpers.
They differ in how they're installed, how config reaches
the hook, what the host exposes to a hook, and (consequently) which span
properties can be populated.

## Runtimes at a glance

| | Claude Code | Cursor | Codex | Copilot CLI | Copilot app |
|---|---|---|---|---|---|
| `gen_ai.harness.name` | `claude-code` | `cursor` | `codex` | `github-copilot-cli` | `github-copilot-app` |
| `gen_ai.provider.name` | `anthropic` fallback + per-model | per-model only | `openai` fallback + per-model | per-model only | per-model only |
| Default `service.name` / agent name | `claude-code` | `cursor` | `codex` | `github-copilot-cli` | `github-copilot-app` |
| Entrypoint | `cmd/claude-on-event` | `cmd/cursor-on-event` | `cmd/codex-on-event` | `cmd/copilot-on-event` | `cmd/copilot-app-on-event` |
| Config file | `~/.claude/dash0-agent-plugin.local.md` (or `.claude/…`) | `~/.cursor/dash0-agent-plugin.local.md` (or `.cursor/…`) | `~/.codex/dash0-agent-plugin.local.md` (or `.codex/…`) | `~/.copilot/dash0-agent-plugin.local.md` (global only) | `~/.copilot/dash0-agent-plugin.local.md` (or `.copilot/…`), shared with the CLI |
| Per-session state dir | `$CLAUDE_PLUGIN_DATA` (required) | `$CURSOR_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/cursor` | `$CODEX_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/codex` | `$COPILOT_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/copilot` | `$COPILOT_APP_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/copilot-app` |
| Hooks registered in | plugin manifest `claude/hooks.json` | `~/.cursor/hooks.json` (merged) | `~/.codex/config.toml` (managed block) | plugin package `copilot/hooks.json` | session extension `copilot-app/extension.mjs` (SDK `joinSession`) |
| Wired hook events | 24 | 9 | 10 | 4 | 4, synthesized from the session event stream |
| Supported OS/arch | `darwin`,`linux`,`windows` × `amd64`,`arm64` | same | same | same | same |
| Windows hook invocation | `claude-on-event.sh` under Git Bash (required) | `cursor-on-event.ps1` — Cursor runs hook commands through PowerShell | `codex-on-event.ps1` via `commandWindows` | `copilot-on-event.ps1` via the `powershell` key | `copilot-app-on-event.ps1`, spawned by the extension through `powershell.exe` |
| Unsupported platform | hook fails | hook fails | hook fails | fails open (untraced) | fails open (untraced) |

`›` reads as "else". Of the three prefixed variables only `COPILOT_PLUGIN_DATA` is
set by an agent today, and Copilot's bootstrap reads the same one, so its binary
cache and session state share a root. Codex sets bare `PLUGIN_DATA` instead:
`codex/codex-on-event.sh` caches the binary under it but never exports it, so for a
marketplace install the cache and the session state sit in different roots.

### OpenCode V2

OpenCode uses a persistent V2-only stream consumer with the shared exporter:

| Capability | OpenCode V2 |
|---|---|
| Harness / entrypoint | `opencode-v2` / `cmd/opencode-v2-on-event` |
| Plugin API | `Plugin.define`, pinned `@opencode/plugin` 2.0.22 |
| Configuration | `opencode.json` `plugins[].package` + `options`; `.opencode-v2/dash0-agent-plugin.local.md`; `OPENCODE_V2_PLUGIN_OPTION_*`, then non-secret `DASH0_*` fallbacks |
| State | `$OPENCODE_V2_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/opencode-v2` |
| Delivery | `opencode plugin add @dash0/agent-plugin-opencode-v2` (npm) or a clone path; shell or PowerShell downloads the checksummed release binary; `executable` selects a local build |
| Configure | Bundled `dash0-configure` skill, registered by the plugin, writes `.opencode-v2/dash0-agent-plugin.local.md` |
| Privacy defaults | `omit_io: true`; `omit_user_info: false`; `omit_identity_fallback: false` |
| Removal | `opencode plugin remove @dash0/agent-plugin-opencode-v2` (or remove the `plugins` entry), then `opencode reload` |
| Reload limitation | Live stream only; a reload may miss events and plugin context has no reliable replay API |
| MCP server attribute | No: MCP tools keep the flattened `<server>_<tool>` name; the plugin API cannot tell them from a local tool in the same namespace |

The release workflow publishes the package to npm. See
[`opencode-v2/README.md`](opencode-v2/README.md) for installation and all options.

### GitHub Copilot app

The Copilot app is not a hook runtime. Its extension (`copilot-app/extension.mjs`)
joins each session through the SDK, buffers the session events of a user turn,
and hands them to the binary at `session.idle` as one `turnEnd` payload — the
app's events already carry usage, response text, tool calls and the sub-agent
tree, so there is no native-OTel file and no launch function.

## Configuration options

Frontmatter keys in the `.local.md` file. The shell wrapper (`<runtime>/<runtime>-on-event.sh`)
parses them and exports the env vars the binary reads. "No (env only)" means the
wrapper doesn't parse that key from the file, so it must be set as an environment
variable instead.

| Option (file key) | Claude Code | Cursor | Codex | Copilot CLI | Copilot app | Notes |
|---|---|---|---|---|---|---|
| `otlp_url` | Yes | Yes | Yes | Yes | Yes | Dash0 OTLP ingress. Empty ⇒ telemetry off. |
| `auth_token` | Yes | Yes | Yes | Yes | Yes | Secure var only, no `DASH0_*` fallback: `{CLAUDE,CURSOR,CODEX,COPILOT}_PLUGIN_OPTION_AUTH_TOKEN`. |
| `auth_token_keychain_service` (+ `_account`) | Yes | No | No | No | No | macOS only. Reads the token from a named keychain item instead of storing it, so managed rollouts ship a pointer rather than the secret. Does not restrict same-user process access. Other runtimes read `auth_token` in plaintext ([SIG-261](https://linear.app/dash0/issue/SIG-261)). |
| `dataset` | Yes | Yes | Yes | Yes | Yes | `Dash0-Dataset` header. |
| `agent_name` | Yes | Yes | Yes | Yes | Yes | → `service.name` / `gen_ai.agent.name`. |
| `team_name` | Yes | Yes | Yes | Yes | Yes | → `dash0.team.name`. |
| `omit_io` | Yes | Yes | Yes | Yes | Yes | Binary default `true` (redact prompts + tool I/O).¹ |
| `omit_user_info` | Yes | Yes | Yes | Yes | Yes | Default `false`. |
| `omit_identity_fallback` | Yes | Yes | Yes | Yes | Yes | Default `false`. When `true`, only a real `git config user.name` is reported; the OS-account fallback is dropped. |
| `enabled` | Yes | Yes | Yes | Yes | Yes | `false` ⇒ wrapper exits, plugin off for that scope. |
| `debug` | Yes | Yes | Yes | Yes | Yes | |
| `debug_file` | Yes | Yes | Yes | Yes | Yes | |
| `show_session_link` | Yes (plugin option / env) | No | No | No | No | Claude-only feature; not parsed from `.local.md` — set via `/plugin → Configure` or `DASH0_SHOW_SESSION_LINK`. Cursor/Codex/Copilot binaries don't consume it. |

¹ The Cursor and Codex README example configs show `omit_io: false`, but the installers
don't write the key. With no explicit setting the binary default (`true`) applies on all
five runtimes.

## Configuration sources & precedence

| | Claude Code | Cursor | Codex | Copilot CLI | Copilot app |
|---|---|---|---|---|---|
| Plugin UI (`/plugin → Configure`) | CLI: Yes (token → OS keychain); Desktop: see caveat below | No | No | No | No |
| `pluginConfigs` in `settings.json` | CLI: user + managed + `--settings`; project ignored since v2.1.207 | No | No | No | No |
| `.local.md` config file | Yes (project > user) | Yes (project > user) | Yes (project > user) | Yes (global only) | Yes (project > user) |
| `DASH0_*` env fallback (non-secret) | Yes (after `CLAUDE_PLUGIN_OPTION_*`) | Yes | Yes | Yes | Yes |

Precedence, highest wins:

- **Claude Code CLI:** `pluginConfigs` (managed and user settings; `--settings` is also read) → `.local.md` (project file if present, else user) → `DASH0_*`. Project `.claude/settings.json` and `.claude/settings.local.json` do not supply `pluginConfigs` in v2.1.207+; project `enabledPlugins` is still honored.
- **Cursor / Codex:** `.local.md` (project → user) → `DASH0_*`
- **Copilot CLI:** `.local.md` (global only) → `DASH0_*`
- **Copilot app:** `.local.md` (project → user) → `DASH0_*`. The app strips secret-looking variables from an extension's environment, so the token belongs in the file.

Config files never merge across scopes: if a project file exists, the user file is ignored entirely.

Claude Desktop has a documented plugin identity mismatch with marketplace-keyed Plugin UI configuration. Use the `.local.md` file for `team_name` and verify in a new session; Desktop end-to-end behavior is not verified here. See the [Claude configuration guide](.claude-plugin/README.md#configuration) for exact CLI identities, reload steps, and the config-file workaround.

## Transferred span properties

Two span types are produced — `chat <model>` / `invoke_agent <type>` and
`execute_tool <name>` — all `SpanKind=Internal`. Logs, metrics, and a standalone
`session_start` span exist in code but are not wired for real sessions (only the
demo generator uses them).

| Property / capability | Claude Code | Cursor | Codex | Copilot CLI | Copilot app | Notes |
|---|---|---|---|---|---|---|
| Chat (LLM) span | Yes | Yes | Yes | Yes | Yes | |
| Tool-call span | Yes | Yes | Yes | Yes | Yes | Copilot: sourced from the native-OTel file, not hooks. |
| `gen_ai.request.model` | Yes | Yes (`default`→`cursor-auto`) | Yes | Yes | Yes (`auto` when auto-selected) | |
| Input / output tokens | Yes | Yes | Yes | Yes | Yes | |
| `cache_read.input_tokens` | Yes | Yes | Yes | Yes | Yes | |
| `cache_creation.input_tokens` | Yes | Yes | No | No | Yes | Codex/Copilot don't report it. |
| Reasoning tokens | Yes | No | No | Yes | Yes | Claude reads `output_tokens_details.thinking_tokens`; Codex parses but doesn't emit; Copilot reads its own field. Claude and Copilot share the key `gen_ai.usage.reasoning.output_tokens` and both emit it only when > 0, so one query covers both and absence means no thinking. |
| Reasoning level (`gen_ai.request.reasoning.level`) | Yes | No | No | No | No | Claude Code puts `effort` on every span-producing payload. The request-side counterpart to the row above: which setting bought that thinking. No other runtime reports a level to the hook — Codex's rollout carries reasoning *tokens* but no effort field. |
| Sub-agent `invoke_agent` span + parenting | Yes | Partial | Yes | Yes | Yes | Cursor: `subagentStart` dropped; the stop span dangles under the chat span. Copilot: sourced from the native-OTel file, since its hooks give a sub-agent a session of its own with nothing linking it to the parent — the tree is `chat → execute_tool task → invoke_agent → execute_tool`, and `gen_ai.agent.id` is the spawning `call_…` id. Copilot CLI: sub-agent chat rounds still fold into the parent turn (flat token attribution), so the agent span carries no usage of its own. Copilot app: the agent span carries the sub-agent's own usage and model, and the chat span only the main agent's. |
| MCP server attribute (`dash0.gen_ai.tool.mcp_server`) | Yes (real server) | Partial (placeholder `cursor`) | Yes (real server) | Yes (real server) | Yes (real server) | |
| Tool-call duration | Native | Native | Reconstructed from `PreToolUse` | Native (from OTel file) | Native (session events) | |
| Session title (`gen_ai.conversation.name`) | Yes | No | No | No | No | Only Claude has a transcript reader. |
| Prompt / response content (`gen_ai.input/output.messages`) | Yes | Yes | Yes | Yes | Yes | Gated by `omit_io`, truncated at 16 KB. Copilot response text comes from the native-OTel file. |
| VCS + code enrichment (repo / branch / PR / issue / commit / lines / bash-family / skill) | Yes | Yes | Yes | Yes | Yes | Shared pipeline extractors. Copilot has no per-edit line counts (`apply_patch` carries no `structuredPatch`). Codex reaches the skill half by a different road: it loads a skill by injecting it into the context rather than calling a `Skill` tool, so it is read from the rollout and lands on the `chat` span, never an `execute_tool` one. |
| User identity (`user.name` / `user.email` / `dash0.gen_ai.user.identity.source`) | Yes | Yes | Yes | Yes | Yes | `git config user.name`, falling back to the OS account (`identity.source=os`). Emitted outside a git repo too. |
| Billing mode (`dash0.gen_ai.billing_mode`, `dash0.gen_ai.plan_type`) | Yes | No | Yes | No | No | Cost is list price × tokens, which is not spend on a subscription. All five are predominantly sold as subscriptions, so absence means "undetermined", never "billed per token". Claude Code follows its own documented auth precedence (env credentials outrank `~/.claude.json`) and can also report `api` or `metered_external` — the latter paired with `dash0.gen_ai.billing_provider` naming the vendor (`bedrock`/`vertex`/`foundry`/`gateway`); Codex reads the rollout and only ever says `subscription`/`unknown`. Copilot is per-seat, so its figure is *never* spend. |
| Rate-limit windows (`dash0.gen_ai.rate_limit.{primary,secondary}.*`, `.reached_type`) | No | No | Yes | No | No | Only Codex persists an allowance snapshot locally. Claude Code enforces windows but does not write them to disk; a 429 in its transcript is the only after-the-fact signal. |
| Overage credits (`dash0.gen_ai.credits.*`) | No | No | Yes | No | No | Codex CLI ≥ ~14 Jul 2026. Claude Code's `overageCreditGrantCache` is grant *eligibility*, a different concept, and must not reuse these keys. |
| Usage source | Claude JSONL transcript | `afterAgentResponse` hook | Codex rollout file | Native-OTel file (per turn) | Session events (`assistant.usage`, per model round) | |

## Installation options

| | Claude Code | Cursor | Codex | Copilot CLI | Copilot app |
|---|---|---|---|---|---|
| Marketplace | `/plugin install dash0@…` | No (local-plugin dir scan) | `codex plugin add dash0-agent-plugin@dash0` | `copilot plugin install dash0-agent-plugin@dash0` (after `marketplace add`) | No |
| `curl \| bash` installer | No | `install-cursor.sh`, `install-cursor.ps1` on Windows | `install-codex.sh`, `install-codex.ps1` on Windows | No (marketplace only) | No |
| Uninstaller | via `/plugin` | `uninstall-cursor.sh`, `uninstall-cursor.ps1` on Windows | `uninstall-codex.sh`, `uninstall-codex.ps1` on Windows | via `copilot plugin` | delete the extension folder |
| Local dev | `claude --plugin-dir …` ([guide](claude/README.md)) | symlink into `~/.cursor/plugins/local/` ([guide](cursor/README.md)) | `emit-codex-hooks` ([guide](codex/README.md#build--run-locally)) | `copilot-local-dev` skill ([guide](copilot/README.md#build--run-locally)) | copy into `.github/extensions/` ([guide](copilot-app/README.md#build--run-locally)) |
| Binary delivery | download + checksum (`on-event.sh`) | download + checksum (`cursor-on-event.sh`, `.ps1` on Windows) | download + checksum (`codex-on-event.sh`, `.ps1` on Windows) | download + checksum (`copilot-on-event.sh`, `.ps1` on Windows) | download + checksum (`copilot-app-on-event.sh`, `.ps1` on Windows) |
| Hook trust step | None | None | Yes — reproduced trust-hash in `config.toml` (installer) or manual `/hooks` (marketplace path) | None (restart `copilot`) | None (new session) |
| Extra requirement | Git for Windows, on Windows only | `jq`, except on Windows | — | launch function (native OTel) via `dash0-configure`; bash, zsh, or PowerShell | `install_extension`, or a copy in `~/.copilot/extensions/` |

## Debugging

| | Claude Code | Cursor | Codex | Copilot CLI | Copilot app |
|---|---|---|---|---|---|
| Enable via config file | `debug` / `debug_file` | same | same | same | same |
| Enable via env | `DASH0_DEBUG` / `DASH0_DEBUG_FILE` | same | same | same | same |
| Output | `[dash0:trace\|log\|metric]` to stderr and/or file | same | same | same | same |
| stderr actually visible | No — hooks exit 0 and Claude Code only surfaces hook stderr under `claude --debug`, so `debug_file` is required to see anything | Yes | Yes | Yes | No: extension stderr goes to the app log, so use `debug_file` |
| Runs pipeline without a backend (empty `otlp_url`) | Yes (when debug on) | Yes | Yes | Yes | Yes |
| Primary path | config file | config file | config file | config file | config file |

## Error handling

Shared principle: **telemetry never breaks the agent loop.** `pipeline.Process`
swallows export errors; each hook sends synchronously with a 5s timeout, 2 attempts,
and a 500ms retry delay.

| | Claude Code | Cursor | Codex | Copilot CLI | Copilot app |
|---|---|---|---|---|---|
| Wrapper on failure | `set -euo pipefail`; may `exit 1` on download/checksum error | fail-open, `exit 0` | fail-open, `exit 0` | fail-open, `exit 0` | fail-open, `exit 0` |
| Binary on `run()` error | logs stderr, `exit 1` | logs stderr, `exit 0` | logs stderr, `exit 0` | logs stderr, `exit 0` | logs stderr, `exit 0` |
| Rationale | Claude tolerates a non-zero observational-hook exit | Cursor blocks on non-zero when `failClosed` | Codex may block on non-zero | Copilot's tool hooks are fail-closed (non-zero blocks) | the extension ignores the exit code; fail-open matches the CLI |
| Connectivity check (SessionStart) | Yes | Yes | Yes | Yes | Yes |
| Missing `session_id` | random ID + `dash0.warning` | same | same | same | same |

## User notifications

The pipeline produces status messages (e.g. the `dash0: connected → <session
link>` welcome banner) uniformly, but only **Claude Code** and the **Copilot app** can show them to the
user at session start; the app's extension writes them to the session timeline.
The others expose only a model-context field there, or a diagnostic log the user
doesn't normally see, so the banner does not render there.

| Agent | User-visible message | Model-context injection | Notes |
|-------|----------------------|-------------------------|-------|
| Claude Code | `systemMessage` (any hook) | `additionalContext` | Full support. |
| Cursor | `user_message` — only when a hook **denies** an action | `additional_context` (sessionStart) | No unblocked startup banner. [docs](https://cursor.com/docs/hooks.md) |
| Codex | none (hook stderr not surfaced; `notify` is OS-only) | none | Nothing user-visible. [docs](https://learn.chatgpt.com/docs/config-file/config-reference) |
| Copilot CLI | none at sessionStart (stderr only on exit 2) | `additionalContext` (sessionStart) | Open bug [copilot-cli#1352](https://github.com/github/copilot-cli/issues/1352). [docs](https://docs.github.com/en/copilot/reference/hooks-reference) |
| Copilot app | `session.log` from the extension (timeline notice) | none | The extension surfaces the binary's `dash0:` stderr lines from sessionStart. |

For Cursor, Codex and the Copilot CLI, injecting the session link as model context is
the only portable fallback — it lets the agent surface the link if asked, but
does not display it directly.
