# Feature support matrix across coding agents

Five runtimes ship today — **Claude Code**, **Cursor**, **OpenAI Codex**,
**GitHub Copilot CLI**, and **OpenCode** — on one shared Go pipeline
(`cmd/*/main.go` → `internal/pipeline` → `internal/otlp`), with each agent's
configuration resolved by `internal/harness`. They differ in how they're
installed, how config reaches the hook, what the host exposes to a hook, and
(consequently) which span properties can be populated.

OpenCode is the exception to "hook": it has no hook mechanism at all. Its events
come from an in-process TypeScript plugin that translates OpenCode's event bus
into the same canonical JSON the other four hooks produce and pipes it into the
same kind of shell wrapper, so everything downstream of the wrapper is shared.

## Runtimes at a glance

| | Claude Code | Cursor | Codex | Copilot CLI | OpenCode |
|---|---|---|---|---|---|
| `gen_ai.harness.name` | `claude-code` | `cursor` | `codex` | `github-copilot-cli` | `opencode` |
| `gen_ai.provider.name` | `anthropic` fallback + per-model | per-model only | `openai` fallback + per-model | per-model only | per-model only |
| Default `service.name` / agent name | `claude-code` | `cursor` | `codex` | `github-copilot-cli` | `opencode` |
| Entrypoint | `cmd/claude-on-event` | `cmd/cursor-on-event` | `cmd/codex-on-event` | `cmd/copilot-on-event` | `cmd/opencode-on-event` |
| Config file | `~/.claude/dash0-agent-plugin.local.md` (or `.claude/…`) | `~/.cursor/dash0-agent-plugin.local.md` (or `.cursor/…`) | `~/.codex/dash0-agent-plugin.local.md` (or `.codex/…`) | `~/.copilot/dash0-agent-plugin.local.md` (global only) | `~/.config/opencode/dash0-agent-plugin.local.md` (or `.opencode/…`) |
| Per-session state dir | `$CLAUDE_PLUGIN_DATA` (required) | `$CURSOR_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/cursor` | `$CODEX_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/codex` | `$COPILOT_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/copilot` | `$OPENCODE_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/opencode` |
| Hooks registered in | plugin manifest `claude/hooks.json` | `~/.cursor/hooks.json` (merged) | `~/.codex/config.toml` (managed block) | plugin package `copilot/hooks.json` | nothing — the plugin file in `~/.config/opencode/plugin/` is auto-loaded, or the package is named in `opencode.json`'s `plugin` array |
| Wired hook events | 24 | 9 | 10 | 4 | 9 canonical, translated from 6 bus events, one `chat.message` hook, and plugin shutdown |
| Supported OS/arch | `darwin`,`linux`,`windows` × `amd64`,`arm64` | same | same | same | `darwin`,`linux` × `amd64`,`arm64` — no Windows build |
| Windows hook invocation | `claude-on-event.sh` under Git Bash (required) | `cursor-on-event.ps1` — Cursor runs hook commands through PowerShell | `codex-on-event.ps1` via `commandWindows` | `copilot-on-event.ps1` via the `powershell` key | none |
| Unsupported platform | hook fails | hook fails | hook fails | fails open (untraced) | fails open (untraced) |

`›` reads as "else". Of the four prefixed variables only `COPILOT_PLUGIN_DATA` is
set by an agent today, and Copilot's bootstrap reads the same one, so its binary
cache and session state share a root. Codex sets bare `PLUGIN_DATA` instead:
`codex/codex-on-event.sh` caches the binary under it but never exports it, so for a
marketplace install the cache and the session state sit in different roots.

## Configuration options

Frontmatter keys in the `.local.md` file. The shell wrapper (`<runtime>/<runtime>-on-event.sh`)
parses them and exports the env vars the binary reads. "No (env only)" means the
wrapper doesn't parse that key from the file, so it must be set as an environment
variable instead.

| Option (file key) | Claude Code | Cursor | Codex | Copilot CLI | OpenCode | Notes |
|---|---|---|---|---|---|---|
| `otlp_url` | Yes | Yes | Yes | Yes | Yes | Dash0 OTLP ingress. Empty ⇒ telemetry off. |
| `auth_token` | Yes | Yes | Yes | Yes | Yes | Secure var only, no `DASH0_*` fallback: `{CLAUDE,CURSOR,CODEX,COPILOT,OPENCODE}_PLUGIN_OPTION_AUTH_TOKEN`. |
| `auth_token_keychain_service` (+ `_account`) | Yes | No | No | No | Yes | macOS only. Reads the token from a named keychain item instead of storing it, so managed rollouts ship a pointer rather than the secret. Does not restrict same-user process access. Cursor, Codex and Copilot read `auth_token` in plaintext ([SIG-261](https://linear.app/dash0/issue/SIG-261)). |
| `dataset` | Yes | Yes | Yes | Yes | Yes | `Dash0-Dataset` header. |
| `agent_name` | Yes | Yes | Yes | Yes | Yes | → `service.name` / `gen_ai.agent.name`. |
| `team_name` | Yes | Yes | Yes | Yes | Yes | → `dash0.team.name`. |
| `omit_io` | Yes | Yes | Yes | Yes | Yes | Binary default `true` (redact prompts + tool I/O).¹ The only content switch on the first four runtimes; on OpenCode it is the fallback the four dimensions layer over. |
| `prompts` / `tools` / `skills` / `agents` | No | No | No | No | Yes | OpenCode only.² Four independent privacy dimensions, each `disabled` \| `limited` \| `full`. |
| `omit_user_info` | Yes | Yes | Yes | Yes | Yes | Default `false`. |
| `omit_identity_fallback` | Yes | Yes | Yes | Yes | Yes | Default `false`. When `true`, only a real `git config user.name` is reported; the OS-account fallback is dropped. |
| `enabled` | Yes | Yes | Yes | Yes | Yes | `false` ⇒ wrapper exits, plugin off for that scope. |
| `debug` | Yes | Yes | Yes | Yes | Yes | |
| `debug_file` | Yes | Yes | Yes | Yes | Yes | |
| `show_session_link` | Yes (plugin option / env) | No | No | No | No | Claude-only option; not parsed from `.local.md` — set via `/plugin → Configure` or `DASH0_SHOW_SESSION_LINK`. The other four binaries don't consume it. OpenCode needs no switch: it renders the link as a TUI toast unconditionally, and headless runs have no TUI to render it in. |

¹ The Cursor and Codex README example configs show `omit_io: false`, but the installers
don't write the key. With no explicit setting the binary default (`true`) applies on all
five runtimes.

² The four dimensions live in the shared pipeline but only the OpenCode entrypoint
exposes them as configuration, so the spans the other four runtimes export are unchanged
by them — `omit_io` alone resolves their posture, exactly as before. On OpenCode the
dimensions layer over `omit_io` rather than replacing it: an explicitly set dimension
wins, else `omit_io` speaks for `prompts` and `tools` (`true` ⇒ both `limited`,
`false` ⇒ both `full`), and `skills` and `agents` default to `limited` since `omit_io`
never spoke for them. See [opencode/README.md](./opencode/README.md#telemetry-privacy).

## Configuration sources & precedence

| | Claude Code | Cursor | Codex | Copilot CLI | OpenCode |
|---|---|---|---|---|---|
| Plugin UI (`/plugin → Configure`) | Yes (token → OS keychain) | No | No | No | No |
| `pluginConfigs` in `settings.json` | Yes (user + project) | No | No | No | No |
| `.local.md` config file | Yes (project > user) | Yes (project > user) | Yes (project > user) | Yes (global only) | Yes (project > user) |
| `DASH0_*` env fallback (non-secret) | Yes (after `CLAUDE_PLUGIN_OPTION_*`) | Yes | Yes | Yes | Yes (after `OPENCODE_PLUGIN_OPTION_*`) |

Precedence, highest wins:

- **Claude Code:** `settings.json` (project → user) → `.local.md` (project → user) → `DASH0_*`
- **Cursor / Codex / OpenCode:** `.local.md` (project → user) → `DASH0_*`
- **Copilot CLI:** `.local.md` (global only) → `DASH0_*`

Config files never merge across scopes: if a project file exists, the user file is ignored entirely.

## Transferred span properties

Two span types are produced — `chat <model>` / `invoke_agent <type>` and
`execute_tool <name>` — all `SpanKind=Internal`. Logs, metrics, and a standalone
`session_start` span exist in code but are not wired for real sessions (only the
demo generator uses them).

| Property / capability | Claude Code | Cursor | Codex | Copilot CLI | OpenCode | Notes |
|---|---|---|---|---|---|---|
| Chat (LLM) span | Yes | Yes | Yes | Yes | Yes | |
| Tool-call span | Yes | Yes | Yes | Yes | Yes | Copilot: sourced from the native-OTel file, not hooks. |
| `gen_ai.request.model` | Yes | Yes (`default`→`cursor-auto`) | Yes | Yes | Yes | |
| Input / output tokens | Yes | Yes | Yes | Yes | Yes | |
| `cache_read.input_tokens` | Yes | Yes | Yes | Yes | Yes | |
| `cache_creation.input_tokens` | Yes | Yes | No | No | Yes | Codex/Copilot don't report it. OpenCode carries the field; whether a given provider ever fills it is the provider's business, and the OpenAI wire format has nowhere to put one. |
| Reasoning tokens | Yes | No | No | Yes | Yes | Claude reads `output_tokens_details.thinking_tokens`; Codex parses but doesn't emit; Copilot reads its own field; OpenCode reads `tokens.reasoning`. All three that emit share the key `gen_ai.usage.reasoning.output_tokens` and emit it only when > 0, so one query covers them and absence means no thinking. |
| Reasoning level (`gen_ai.request.reasoning.level`) | Yes | No | No | No | No | Claude Code puts `effort` on every span-producing payload. The request-side counterpart to the row above: which setting bought that thinking. No other runtime reports a level — Codex's rollout carries reasoning *tokens* but no effort field, and OpenCode's bus events carry neither. |
| Sub-agent `invoke_agent` span + parenting | Yes | Partial | Yes | Yes | Yes | Cursor: `subagentStart` dropped; the stop span dangles under the chat span. Copilot: sourced from the native-OTel file, since its hooks give a sub-agent a session of its own with nothing linking it to the parent — the tree is `chat → execute_tool task → invoke_agent → execute_tool`, and `gen_ai.agent.id` is the spawning `call_…` id. Sub-agent chat rounds still fold into the parent turn (flat token attribution), so the agent span carries no usage of its own. OpenCode gives sub-sessions a real `parentID`, so the sub-agent's own tool spans keep their depth under the `invoke_agent` span instead of flattening onto the turn. |
| MCP server attribute (`dash0.gen_ai.tool.mcp_server`) | Yes (real server) | Partial (placeholder `cursor`) | Yes (real server) | Yes (real server) | Yes (real server) | OpenCode names MCP tools `<server>_<tool>`, resolved against the configured server keys rather than split on the first underscore, so a server whose name contains one still resolves. |
| Tool-call duration | Native | Native | Reconstructed from `PreToolUse` | Native (from OTel file) | Native | |
| Session title (`gen_ai.conversation.name`) | Yes | No | No | No | Yes | Claude has a transcript reader; OpenCode publishes the title on the session record itself. |
| Prompt / response content (`gen_ai.input/output.messages`) | Yes | Yes | Yes | Yes | Yes | Truncated at 16 KB. Gated by `omit_io` on the first four; by the `prompts` dimension on OpenCode. Copilot response text comes from the native-OTel file. |
| VCS + code enrichment (repo / branch / PR / issue / commit / lines / bash-family / skill) | Yes | Yes | Yes | Yes | Yes | Shared pipeline extractors. Copilot has no per-edit line counts (`apply_patch` carries no `structuredPatch`). Codex reaches the skill half by a different road: it loads a skill by injecting it into the context rather than calling a `Skill` tool, so it is read from the rollout and lands on the `chat` span, never an `execute_tool` one. |
| User identity (`user.name` / `user.email` / `dash0.gen_ai.user.identity.source`) | Yes | Yes | Yes | Yes | Yes | `git config user.name`, falling back to the OS account (`identity.source=os`). Emitted outside a git repo too. |
| Billing mode (`dash0.gen_ai.billing_mode`, `dash0.gen_ai.plan_type`) | Yes | No | Yes | No | No | Cost is list price × tokens, which is not spend on a subscription. Claude Code, Cursor, Codex and Copilot are predominantly sold as subscriptions, so absence means "undetermined", never "billed per token". Claude Code follows its own documented auth precedence (env credentials outrank `~/.claude.json`) and can also report `api` or `metered_external` — the latter paired with `dash0.gen_ai.billing_provider` naming the vendor (`bedrock`/`vertex`/`foundry`/`gateway`); Codex reads the rollout and only ever says `subscription`/`unknown`. Copilot is per-seat, so its figure is *never* spend. OpenCode is bring-your-own-key with no vendor of its own, so there is no plan to detect. |
| Rate-limit windows (`dash0.gen_ai.rate_limit.{primary,secondary}.*`, `.reached_type`) | No | No | Yes | No | No | Only Codex persists an allowance snapshot locally. Claude Code enforces windows but does not write them to disk; a 429 in its transcript is the only after-the-fact signal. |
| Overage credits (`dash0.gen_ai.credits.*`) | No | No | Yes | No | No | Codex CLI ≥ ~14 Jul 2026. Claude Code's `overageCreditGrantCache` is grant *eligibility*, a different concept, and must not reuse these keys. |
| Usage source | Claude JSONL transcript | `afterAgentResponse` hook | Codex rollout file | Native-OTel file (per turn) | `message.updated` bus events, summed per turn | |

## Installation options

| | Claude Code | Cursor | Codex | Copilot CLI | OpenCode |
|---|---|---|---|---|---|
| Marketplace | `/plugin install dash0@…` | No (local-plugin dir scan) | `codex plugin add dash0-agent-plugin@dash0` | `copilot plugin install dash0-agent-plugin@dash0` (after `marketplace add`) | No — npm instead: `@dash0/opencode-plugin` in `opencode.json`'s `plugin` array |
| `curl \| bash` installer | No | `install-cursor.sh`, `install-cursor.ps1` on Windows | `install-codex.sh`, `install-codex.ps1` on Windows | No (marketplace only) | `install-opencode.sh` (no Windows script) |
| Uninstaller | via `/plugin` | `uninstall-cursor.sh`, `uninstall-cursor.ps1` on Windows | `uninstall-codex.sh`, `uninstall-codex.ps1` on Windows | via `copilot plugin` | `uninstall-opencode.sh` |
| Local dev | `claude --plugin-dir …` ([guide](claude/README.md)) | symlink into `~/.cursor/plugins/local/` ([guide](cursor/README.md)) | `emit-codex-hooks` ([guide](codex/README.md#build--run-locally)) | `copilot-local-dev` skill ([guide](copilot/README.md#build--run-locally)) | copy the built bundle into `~/.config/opencode/plugin/` ([guide](opencode/README.md#build--run-locally)) |
| Binary delivery | download + checksum (`on-event.sh`) | download + checksum (`cursor-on-event.sh`, `.ps1` on Windows) | download + checksum (`codex-on-event.sh`, `.ps1` on Windows) | download + checksum (`copilot-on-event.sh`, `.ps1` on Windows) | download + checksum (`opencode-on-event.sh`) |
| Hook trust step | None | None | Yes — reproduced trust-hash in `config.toml` (installer) or manual `/hooks` (marketplace path) | None (restart `copilot`) | None |
| Extra requirement | Git for Windows, on Windows only | `jq`, except on Windows | — | launch function (native OTel) via `dash0-configure`; bash, zsh, or PowerShell | npm, on the package path only |

## Debugging

| | Claude Code | Cursor | Codex | Copilot CLI | OpenCode |
|---|---|---|---|---|---|
| Enable via config file | `debug` / `debug_file` | same | same | same | same |
| Enable via env | `DASH0_DEBUG` / `DASH0_DEBUG_FILE` | same | same | same | same |
| Output | `[dash0:trace\|log\|metric]` to stderr and/or file | same | same | same | same |
| stderr actually visible | No — hooks exit 0 and Claude Code only surfaces hook stderr under `claude --debug`, so `debug_file` is required to see anything | Yes | Yes | Yes | No — the plugin pipes the wrapper's stderr on `SessionStart` only, to render the toast, and discards it on every other event, so `debug_file` is required |
| Runs pipeline without a backend (empty `otlp_url`) | Yes (when debug on) | Yes | Yes | Yes | Yes |
| Primary path | config file | config file | config file | config file | config file |

## Error handling

Shared principle: **telemetry never breaks the agent loop.** `pipeline.Process`
swallows export errors; each hook sends synchronously with a 5s timeout, 2 attempts,
and a 500ms retry delay.

| | Claude Code | Cursor | Codex | Copilot CLI | OpenCode |
|---|---|---|---|---|---|
| Wrapper on failure | `set -euo pipefail`; may `exit 1` on download/checksum error | fail-open, `exit 0` | fail-open, `exit 0` | fail-open, `exit 0` | fail-open, `exit 0` |
| Binary on `run()` error | logs stderr, `exit 1` | logs stderr, `exit 0` | logs stderr, `exit 0` | logs stderr, `exit 0` | logs stderr, `exit 0` |
| Rationale | Claude tolerates a non-zero observational-hook exit | Cursor blocks on non-zero when `failClosed` | Codex may block on non-zero | Copilot's tool hooks are fail-closed (non-zero blocks) | the plugin spawns the wrapper fire-and-forget and never awaits it, so no exit code can reach the agent loop |
| Connectivity check (SessionStart) | Yes | Yes | Yes | Yes | Yes |
| Missing `session_id` | random ID + `dash0.warning` | same | same | same | same |

The `opencode-on-event session-url` subcommand is the one path that exits
non-zero on failure. It answers a question the user asked through
`/open-session`, so silence would be the wrong answer; it produces no telemetry
and cannot run inside a session's event flow.

## User notifications

The pipeline produces status messages (e.g. the `dash0: connected → <session
link>` welcome banner) uniformly, but only **Claude Code** and **OpenCode** can
show them to the user at session start. The others expose only a model-context
field there, or a diagnostic log the user doesn't normally see.

| Agent | User-visible message | Model-context injection | Notes |
|-------|----------------------|-------------------------|-------|
| Claude Code | `systemMessage` (any hook) | `additionalContext` | Full support. |
| Cursor | `user_message` — only when a hook **denies** an action | `additional_context` (sessionStart) | No unblocked startup banner. [docs](https://cursor.com/docs/hooks.md) |
| Codex | none (hook stderr not surfaced; `notify` is OS-only) | none | Nothing user-visible. [docs](https://learn.chatgpt.com/docs/config-file/config-reference) |
| Copilot CLI | none at sessionStart (stderr only on exit 2) | `additionalContext` (sessionStart) | Open bug [copilot-cli#1352](https://github.com/github/copilot-cli/issues/1352). [docs](https://docs.github.com/en/copilot/reference/hooks-reference) |
| OpenCode | `client.tui.showToast` (session start) | none | The plugin reads the wrapper's stderr and renders each line as a toast. Headless `opencode run` has no TUI, so the call fails and is swallowed. |

For Cursor, Codex and Copilot CLI, injecting the session link as model context is
the only portable fallback — it lets the agent surface the link if asked, but
does not display it directly.
