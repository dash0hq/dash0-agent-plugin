# Feature support matrix across coding agents

Four runtimes ship today — **Claude Code**, **Cursor**, **OpenAI Codex**, and
**GitHub Copilot CLI** — on one shared Go pipeline (`cmd/*/main.go` →
`internal/pipeline` → `internal/otlp`), with each agent's configuration resolved
by `internal/harness`. They differ in how they're installed, how config reaches
the hook, what the host exposes to a hook, and (consequently) which span
properties can be populated.

## Runtimes at a glance

| | Claude Code | Cursor | Codex | Copilot CLI |
|---|---|---|---|---|
| `gen_ai.harness.name` | `claude-code` | `cursor` | `codex` | `github-copilot-cli` |
| `gen_ai.provider.name` | `anthropic` fallback + per-model | per-model only | `openai` fallback + per-model | per-model only |
| Default `service.name` / agent name | `claude-code` | `cursor` | `codex` | `github-copilot-cli` |
| Entrypoint | `cmd/claude-on-event` | `cmd/cursor-on-event` | `cmd/codex-on-event` | `cmd/copilot-on-event` |
| Config file | `~/.claude/dash0-agent-plugin.local.md` (or `.claude/…`) | `~/.cursor/dash0-agent-plugin.local.md` (or `.cursor/…`) | `~/.codex/dash0-agent-plugin.local.md` (or `.codex/…`) | `~/.copilot/dash0-agent-plugin.local.md` (global only) |
| Per-session state dir | `$CLAUDE_PLUGIN_DATA` (required) | `$CURSOR_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/cursor` | `$CODEX_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/codex` | `$COPILOT_PLUGIN_DATA` › `$DASH0_PLUGIN_DATA` › `~/.local/state/dash0-agent-plugin/copilot` |
| Hooks registered in | plugin manifest `claude/hooks.json` | `~/.cursor/hooks.json` (merged) | `~/.codex/config.toml` (managed block) | plugin package `copilot/hooks.json` |
| Wired hook events | 24 | 9 | 10 | 4 |
| Supported OS/arch | `darwin`,`linux` × `amd64`,`arm64` | same | same | same |
| Unsupported platform | hook fails | hook fails | hook fails | fails open (untraced) |

`›` reads as "else". Of the three prefixed variables only `COPILOT_PLUGIN_DATA` is
set by an agent today, and Copilot's bootstrap reads the same one, so its binary
cache and session state share a root. Codex sets bare `PLUGIN_DATA` instead:
`codex/codex-on-event.sh` caches the binary under it but never exports it, so for a
marketplace install the cache and the session state sit in different roots.

## Configuration options

Frontmatter keys in the `.local.md` file. The shell wrapper (`<runtime>/<runtime>-on-event.sh`)
parses them and exports the env vars the binary reads. "No (env only)" means the
wrapper doesn't parse that key from the file, so it must be set as an environment
variable instead.

| Option (file key) | Claude Code | Cursor | Codex | Copilot CLI | Notes |
|---|---|---|---|---|---|
| `otlp_url` | Yes | Yes | Yes | Yes | Dash0 OTLP ingress. Empty ⇒ telemetry off. |
| `auth_token` | Yes | Yes | Yes | Yes | Secure var only, no `DASH0_*` fallback: `{CLAUDE,CURSOR,CODEX,COPILOT}_PLUGIN_OPTION_AUTH_TOKEN`. |
| `auth_token_keychain_service` (+ `_account`) | Yes | No | No | No | macOS only. Reads the token from a named keychain item instead of storing it, so managed rollouts ship a pointer rather than the secret. Does not restrict same-user process access. Other runtimes read `auth_token` in plaintext ([SIG-261](https://linear.app/dash0/issue/SIG-261)). |
| `dataset` | Yes | Yes | Yes | Yes | `Dash0-Dataset` header. |
| `agent_name` | Yes | Yes | Yes | Yes | → `service.name` / `gen_ai.agent.name`. |
| `team_name` | Yes | Yes | Yes | Yes | → `dash0.team.name`. |
| `omit_io` | Yes | Yes | Yes | Yes | Binary default `true` (redact prompts + tool I/O).¹ |
| `omit_user_info` | Yes | Yes | Yes | Yes | Default `false`. |
| `omit_identity_fallback` | Yes | Yes | Yes | Yes | Default `false`. When `true`, only a real `git config user.name` is reported; the OS-account fallback is dropped. |
| `enabled` | Yes | Yes | Yes | Yes | `false` ⇒ wrapper exits, plugin off for that scope. |
| `debug` | Yes | Yes | Yes | Yes | |
| `debug_file` | Yes | Yes | Yes | Yes | |
| `show_session_link` | Yes (plugin option / env) | No | No | No | Claude-only feature; not parsed from `.local.md` — set via `/plugin → Configure` or `DASH0_SHOW_SESSION_LINK`. Cursor/Codex/Copilot binaries don't consume it. |

¹ The Cursor and Codex README example configs show `omit_io: false`, but the installers
don't write the key. With no explicit setting the binary default (`true`) applies on all
four runtimes.

## Configuration sources & precedence

| | Claude Code | Cursor | Codex | Copilot CLI |
|---|---|---|---|---|
| Plugin UI (`/plugin → Configure`) | Yes (token → OS keychain) | No | No | No |
| `pluginConfigs` in `settings.json` | Yes (user + project) | No | No | No |
| `.local.md` config file | Yes (project > user) | Yes (project > user) | Yes (project > user) | Yes (global only) |
| `DASH0_*` env fallback (non-secret) | Yes (after `CLAUDE_PLUGIN_OPTION_*`) | Yes | Yes | Yes |

Precedence, highest wins:

- **Claude Code:** `settings.json` (project → user) → `.local.md` (project → user) → `DASH0_*`
- **Cursor / Codex:** `.local.md` (project → user) → `DASH0_*`
- **Copilot CLI:** `.local.md` (global only) → `DASH0_*`

Config files never merge across scopes: if a project file exists, the user file is ignored entirely.

## Transferred span properties

Two span types are produced — `chat <model>` / `invoke_agent <type>` and
`execute_tool <name>` — all `SpanKind=Internal`. Logs, metrics, and a standalone
`session_start` span exist in code but are not wired for real sessions (only the
demo generator uses them).

| Property / capability | Claude Code | Cursor | Codex | Copilot CLI | Notes |
|---|---|---|---|---|---|
| Chat (LLM) span | Yes | Yes | Yes | Yes | |
| Tool-call span | Yes | Yes | Yes | Yes | Copilot: sourced from the native-OTel file, not hooks. |
| `gen_ai.request.model` | Yes | Yes (`default`→`cursor-auto`) | Yes | Yes | |
| Input / output tokens | Yes | Yes | Yes | Yes | |
| `cache_read.input_tokens` | Yes | Yes | Yes | Yes | |
| `cache_creation.input_tokens` | Yes | Yes | No | No | Codex/Copilot don't report it. |
| Reasoning tokens | Yes | No | No | Yes | Claude reads `output_tokens_details.thinking_tokens`; Codex parses but doesn't emit; Copilot reads its own field. Claude and Copilot share the key `gen_ai.usage.reasoning.output_tokens` and both emit it only when > 0, so one query covers both and absence means no thinking. |
| Reasoning level (`gen_ai.request.reasoning.level`) | Yes | No | No | No | Claude Code puts `effort` on every span-producing payload. The request-side counterpart to the row above: which setting bought that thinking. No other runtime reports a level to the hook — Codex's rollout carries reasoning *tokens* but no effort field. |
| Sub-agent `invoke_agent` span + parenting | Yes | Partial | Yes | Partial | Cursor: `subagentStart` dropped; the stop span dangles under the chat span. Copilot: sub-agent chat rounds fold into the parent turn (flat token attribution); their tool calls re-parent under the spawning `task` span. |
| MCP server attribute (`dash0.gen_ai.tool.mcp_server`) | Yes (real server) | Partial (placeholder `cursor`) | Yes (real server) | Yes (real server) | |
| Tool-call duration | Native | Native | Reconstructed from `PreToolUse` | Native (from OTel file) | |
| Session title (`gen_ai.conversation.name`) | Yes | No | No | No | Only Claude has a transcript reader. |
| Prompt / response content (`gen_ai.input/output.messages`) | Yes | Yes | Yes | Yes | Gated by `omit_io`, truncated at 16 KB. Copilot response text comes from the native-OTel file. |
| VCS + code enrichment (repo / branch / PR / issue / commit / lines / bash-family / skill) | Yes | Yes | Yes | Yes | Shared pipeline extractors. Copilot has no per-edit line counts (`apply_patch` carries no `structuredPatch`). |
| User identity (`user.name` / `user.email` / `dash0.gen_ai.user.identity.source`) | Yes | Yes | Yes | Yes | `git config user.name`, falling back to the OS account (`identity.source=os`). Emitted outside a git repo too. |
| Billing mode (`dash0.gen_ai.billing_mode`, `dash0.gen_ai.plan_type`) | Yes | No | Yes | No | Cost is list price × tokens, which is not spend on a subscription. All four are predominantly sold as subscriptions, so absence means "undetermined", never "billed per token". Claude Code follows its own documented auth precedence (env credentials outrank `~/.claude.json`) and can also report `api` or `metered_external` — the latter paired with `dash0.gen_ai.billing_provider` naming the vendor (`bedrock`/`vertex`/`foundry`/`gateway`); Codex reads the rollout and only ever says `subscription`/`unknown`. Copilot is per-seat, so its figure is *never* spend. |
| Rate-limit windows (`dash0.gen_ai.rate_limit.{primary,secondary}.*`, `.reached_type`) | No | No | Yes | No | Only Codex persists an allowance snapshot locally. Claude Code enforces windows but does not write them to disk; a 429 in its transcript is the only after-the-fact signal. |
| Overage credits (`dash0.gen_ai.credits.*`) | No | No | Yes | No | Codex CLI ≥ ~14 Jul 2026. Claude Code's `overageCreditGrantCache` is grant *eligibility*, a different concept, and must not reuse these keys. |
| Usage source | Claude JSONL transcript | `afterAgentResponse` hook | Codex rollout file | Native-OTel file (per turn) | |

## Installation options

| | Claude Code | Cursor | Codex | Copilot CLI |
|---|---|---|---|---|
| Marketplace | `/plugin install dash0@…` | No (local-plugin dir scan) | `codex plugin add dash0-agent-plugin@dash0` | `copilot plugin install dash0-agent-plugin@dash0` (after `marketplace add`) |
| `curl \| bash` installer | No | `install-cursor.sh` | `install-codex.sh` | No (marketplace only) |
| Uninstaller | via `/plugin` | `uninstall-cursor.sh` | `uninstall-codex.sh` | via `copilot plugin` |
| Local dev | `claude --plugin-dir …` ([guide](claude/README.md)) | symlink into `~/.cursor/plugins/local/` ([guide](cursor/README.md)) | `emit-codex-hooks` ([guide](codex/README.md#build--run-locally)) | `copilot-local-dev` skill ([guide](copilot/README.md#build--run-locally)) |
| Binary delivery | download + checksum (`on-event.sh`) | download + checksum (`cursor-on-event.sh`) | download + checksum (`codex-on-event.sh`) | download + checksum (`copilot-on-event.sh`) |
| Hook trust step | None | None | Yes — reproduced trust-hash in `config.toml` (installer) or manual `/hooks` (marketplace path) | None (restart `copilot`) |
| Extra requirement | — | `jq` | — | launch function (native OTel) via `dash0-configure` |

## Debugging

| | Claude Code | Cursor | Codex | Copilot CLI |
|---|---|---|---|---|
| Enable via config file | `debug` / `debug_file` | same | same | same |
| Enable via env | `DASH0_DEBUG` / `DASH0_DEBUG_FILE` | same | same | same |
| Output | `[dash0:trace\|log\|metric]` to stderr and/or file | same | same | same |
| stderr actually visible | No — hooks exit 0 and Claude Code only surfaces hook stderr under `claude --debug`, so `debug_file` is required to see anything | Yes | Yes | Yes |
| Runs pipeline without a backend (empty `otlp_url`) | Yes (when debug on) | Yes | Yes | Yes |
| Primary path | config file | config file | config file | config file |

## Error handling

Shared principle: **telemetry never breaks the agent loop, and never tells the
user about itself.** `pipeline.Process` swallows export errors; each hook sends
synchronously with a 5s timeout, 2 attempts, and a 500ms retry delay.

Every host renders a nonzero hook exit to the user, and Codex renders only the
number — it discards the hook's stderr for any code other than 0 or 2. So there
is no such thing as a useful failure exit here: the exit status is always 0.

| | Claude Code | Cursor | Codex | Copilot CLI |
|---|---|---|---|---|
| Wrapper on failure | fail-open, `exit 0` | fail-open, `exit 0` | fail-open, `exit 0` | fail-open, `exit 0` |
| Binary on `run()` error | logs stderr, `exit 1`, masked by the wrapper | logs stderr, `exit 0` | logs stderr, `exit 0` | logs stderr, `exit 0` |
| Handoff to the binary | child process (`exec` cannot be made safe under this wrapper's `set -e`) | `exec` + `execfail` | `exec` + `execfail` | `exec` + `execfail` |
| Registration guards a missing wrapper | `\|\| true` | n/a — plugin dir is not version-scoped | breadcrumb + `exit 0` | `\|\| true` |
| Connectivity check (SessionStart) | Yes | Yes | Yes | Yes |
| Missing `session_id` | random ID + `dash0.warning` | same | same | same |

### Unsent data is kept, not dropped

A hook is a fresh process with no retry budget past its own lifetime, so an
unreachable endpoint used to mean the telemetry was gone — quietly, which made
cost analysis short without saying so.

- **Spool** (`internal/spool`). A payload that cannot be sent is written to
  `<state>/spool`, bounded at 512 files and 32 MiB, oldest evicted first. Every
  later invocation drains it oldest-first, at most 25 payloads or 3 seconds per
  invocation, stopping at the first failure. `pipeline.Process` sets
  `Config.SpoolDir`; a `Config` without it behaves exactly as before.
- **Incidents** (`internal/incident`). When the wrapper itself is missing, no Go
  code runs, so the registration appends a line to `<state>/incidents.log`
  instead. The next working invocation reports each one as a `WARN` log record
  (`dash0.plugin.incident.kind`, `.count`, `.detail`, `.first`, `.last`), which is
  the only way a session that ran with the plugin mute becomes visible. Derive
  metrics from those records in the backend rather than emitting metrics here.
- Both live in the per-session state root above, so `uninstall-*.sh` removes them
  with the rest of it.

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
