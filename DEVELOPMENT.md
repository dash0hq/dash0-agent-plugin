# Development

## Releasing

> `scripts/release.sh <version>` executes the following release steps in one go.
>
Releases are automated with [GoReleaser](https://goreleaser.com/) via GitHub Actions. To create a new release, update the version in:

- `.claude-plugin/plugin.json` — `version` field
- `.cursor-plugin/plugin.json` — `version` field
- `copilot/plugin.json` — `version` field
- `.github/plugin/marketplace.json` — `metadata.version` and the plugin entry `version` (Copilot marketplace)
- `claude/claude-on-event.sh` — `VERSION=` line (Claude Code binary downloader)
- `cursor/cursor-on-event.sh` — `VERSION=` line (Cursor binary downloader)
- `codex/codex-on-event.sh` — `VERSION=` line (Codex binary downloader)
- `copilot/copilot-on-event.sh` — `VERSION=` line (Copilot binary downloader; vendored inside the `copilot/` subpath-install package)

> **Renaming a published asset.** The Claude marketplace lists this repo with no
> ref, so `claude plugin install` and `update` take the default branch. A
> checked-in bootstrap is therefore paired with the *last published* release, and a
> script that asks for a name that release does not carry breaks every fresh
> install until the next tag.
>
> `claude/claude-on-event.sh` handles this by trying each name it may have been
> published under, newest first, and using the first that resolves. Each installed
> script asks its own pinned release, and that release carries whichever name it
> was built with, so no commit on the default branch is ever inconsistent.
>
> The Claude asset is already switched: releases from v0.1.25 on publish
> `claude-on-event-<os>-<arch>`, and v0.1.24 and earlier carry the unprefixed
> `on-event-<os>-<arch>`. Drop the `on-event-<os>-<arch>` candidate from the script
> once no supported install can still be pinned to v0.1.24 or earlier. The local
> cache filename stays `on-event-<version>-<os>-<arch>` on purpose, because
> changing it would force every existing install to download again.
>
> The CI job "Release assets exist for configured version" reads the candidates out
> of each bootstrap and requires at least one to exist per platform, so a name that
> nothing publishes cannot pass unnoticed.

`main` is protected, so the script commits the version bump on a `release/v<version>` branch and pushes it — it does **not** push a tag. Open a PR from that branch and merge it.

After the PR is merged, tag the merged commit on `main` manually:

```bash
git checkout main && git pull
git tag v<version>
git push origin v<version>
```

The tag push triggers the release workflow which cross-compiles binaries for `darwin/linux × amd64/arm64` and publishes them to [GitHub Releases](https://github.com/dash0hq/dash0-agent-plugin/releases).
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
> `dash0.gen_ai.user.identity.source` is not identifying and is never hashed.

> `user.name` falls back to the OS account when `git config user.name` is unset, so a
> developer with no git identity is still attributable. Set `omit_identity_fallback` to
> require a real git identity and drop the fallback instead.

### Span shape

| Field | Value |
|---|---|
| Span name | `chat <model>`, `invoke_agent <agent_type>` (sub-agent), or `execute_tool <tool_name>` |
| Span kind | `Internal` (always) |
| Status | `Unset` normally; `Error` (with `exception.message`) on `StopFailure` / `PostToolUseFailure` |
| Trace / parent IDs | random per turn, allocated at prompt submit; tool spans and sub-agents parent to the turn's chat span, except that a tool call carrying `agent_id` parents to the span derived from that id — the `execute_tool Agent` span that launched it — so a sub-agent's work keeps its depth instead of being flattened onto the turn |

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
| `user.name` | Real name, or a 16-hex-char SHA-256 hash when `omit_user_info`                 | From `git config user.name`, else the OS account. |
| `user.email` | git email                                                                      | git-only, never inferred. Omitted when `omit_user_info`. |
| `dash0.gen_ai.user.identity.source` | `git` or `os`                                                                  | Which source `user.name` came from. Emitted whenever a name is. |

The `dash0.gen_ai.vcs.*` keys are only present inside a git repository. The identity keys
(`user.*`, `dash0.gen_ai.user.identity.source`) are independent of it — they are emitted
outside a working tree too, since the user is still the user. Any individual key is
omitted when its value is empty.

### LLM / chat spans (`chat` and `invoke_agent`)

| Key | Value / Example | Notes                          |
|---|---|--------------------------------|
| `gen_ai.operation.name` | `chat` or `invoke_agent`                                             |                                |
| `gen_ai.request.model` | `claude-…`, `gpt-…`, `cursor-auto`, …                                |                                |
| `gen_ai.conversation.name` | Session title                                                        | Claude only (from transcript). Content-gated by `omit_io`: the title is derived from the first prompt. |
| `gen_ai.usage.input_tokens` | integer                                                              |                                |
| `gen_ai.usage.output_tokens` | integer                                                              |                                |
| `gen_ai.usage.cache_read.input_tokens` | integer                                                              |                                |
| `gen_ai.usage.cache_creation.input_tokens` | integer                                                              | Not emitted by Codex.          |
| `dash0.gen_ai.usage.cache_creation.ephemeral_5m.input_tokens` | integer                                                              | Claude only. |
| `dash0.gen_ai.usage.cache_creation.ephemeral_1h.input_tokens` | integer                                                              | Claude only. |
| `gen_ai.usage.reasoning.output_tokens` | integer                                                              | Claude (from the transcript) and Copilot, both only when > 0. A subset of `output_tokens`, not an addition — cost is unaffected, and absence means the turn did no thinking. |
| `dash0.gen_ai.tool.skill.name` | e.g. `writing:unslop`                                                | Claude only, and only on the chat span of a turn a slash command started. See below. |
| `dash0.gen_ai.tool.skill.source` | `command`                                                            | Same rows as above. |
| `gen_ai.input.messages` | JSON: `[{"role":"user","parts":[{"type":"text","content":"…"}]}]`    | Content-gated by `omit_io`.    |
| `gen_ai.output.messages` | JSON: `[{"role":"assistant","parts":[{"type":"text","content":"…"}]}]` | Content-gated by `omit_io`.    |
| `gen_ai.agent.id` | Sub-agent ID                                                         | On`invoke_agent` spans.        |
| `exception.message` | Error text                                                           | On `StopFailure`.              |
| `dash0.gen_ai.billing_mode` | `subscription` \| `api` \| `metered_external` \| `unknown`             | Claude Code + Codex. Set whenever token usage is (Codex: always). Codex only ever says `subscription`/`unknown` — see below. |
| `dash0.gen_ai.billing_provider` | `bedrock` \| `vertex` \| `foundry` \| `gateway`                        | Claude Code only. Present only when the mode is `metered_external`. |
| `dash0.gen_ai.plan_type` | Codex: `free`, `plus`, `pro`. Claude Code: `team_standard`, Max tiers | Claude Code + Codex. Omitted when unreported. Provider vocabulary — display, don't parse. |
| `dash0.gen_ai.rate_limit.{primary,secondary}.used_percent` | float, 0–100                                                         | Codex only. Omitted per slot when unreported. |
| `dash0.gen_ai.rate_limit.{primary,secondary}.window_minutes` | integer (`43200` = 30 days, `300` = 5 hours)                         | Codex only. |
| `dash0.gen_ai.rate_limit.{primary,secondary}.resets_at` | integer, unix seconds                                                | Codex only. |
| `dash0.gen_ai.rate_limit.reached_type` | Which window blocked                                                 | Codex only. Omitted until a limit is actually hit. |
| `dash0.gen_ai.credits.available` | boolean                                                              | Codex only. CLI ≥ ~14 Jul 2026. |
| `dash0.gen_ai.credits.unlimited` | boolean                                                              | Codex only. |
| `dash0.gen_ai.credits.balance` | float                                                                | Codex only. Omitted when unreported. |

#### Skill invocations come by two routes

A skill can be invoked two ways, and they are recorded on different spans.

- **The model chooses it.** Claude Code makes a `Skill` tool call, so there is a
  `PostToolUse` hook and an `execute_tool Skill` span. `dash0.gen_ai.tool.skill.source` is
  `model`.
- **A person types the slash command.** Claude Code expands `/writing:unslop …` before any
  tool runs, so no tool hook fires and no tool span exists. The invocation is reported on
  the turn's `chat` span instead, with `dash0.gen_ai.tool.skill.source` set to `command`.

Both carry `dash0.gen_ai.tool.skill.name` with the same plugin-qualified value, so one query
counts every invocation and `source` splits it by who decided.

The command route is read from the transcript, which is the only place it is recorded:
Claude Code writes a `<command-name>` tag into the turn's user entry, and a skill load
appends an `isMeta` entry naming the skill's base directory. Both are required, and the
command's last colon-separated segment must match that directory's name. That conjunction is
what keeps `/compact` and `/plugin` out of the count — they write the same tag but load no
skill — and what stops a prompt that merely mentions a slash command from counting.

#### Billing mode and rate limits

Cost is computed as provider list price × tokens. On a subscription there is no
per-token price at all — a flat fee buys a rationed allowance and the marginal
token is free — so that figure is a list-price *equivalent*, not spend.
`dash0.gen_ai.billing_mode` tells the consumer which it is. `metered_external` is
the third case: per-token, but metered by somebody else at a rate we cannot see —
a negotiated cloud rate, say — so that figure is neither list-equivalent *nor*
spend.

**An absent `billing_mode` means "undetermined", never "billed per token".** All
four harnesses are predominantly sold as subscriptions; only Claude Code and Codex
expose a detectable signal, and Copilot is per-seat, so its figure is never spend.

##### How it is derived, per harness

**Claude Code — by auth precedence, not by the config file.** Setting
`ANTHROPIC_API_KEY` disables the OAuth flow, and an env-var key takes precedence
over an authenticated subscription, so a stale `oauthAccount` can sit in
`~/.claude.json` while traffic bills per token. Reading `billingType` alone reports
`subscription` for those users — telling a customer their real spend is not real
spend.

The signals are evaluated in the order below, which is **Claude Code's documented
authentication precedence**, not ours — see "Authentication precedence" in its
authentication docs. It is load-bearing: a credential further down the list is not
the one in use.

| Rank | Signal | Mode | Provider | Why |
|---|---|---|---|---|
| 1 | `CLAUDE_CODE_USE_BEDROCK` / `_VERTEX` / `_FOUNDRY` | `metered_external` | `bedrock` / `vertex` / `foundry` | AWS / Google / Microsoft bills, at a rate we cannot see |
| 2 | `ANTHROPIC_AUTH_TOKEN` | `metered_external` | `gateway` | bearer token — an LLM gateway or proxy sits in front |
| 3 | `ANTHROPIC_API_KEY` | `api` | — | direct per-token at list price; the figure **is** spend |
| 4 | `apiKeyHelper` | — | — | **undetectable**, see below |
| 5 | `CLAUDE_CODE_OAUTH_TOKEN` | *from the plan* | — | plan-backed, but a plan is not proof of a subscription |
| 6 | `ANTHROPIC_PROFILE`, or the federation pair | `metered_external` | `gateway` | enterprise WIF — Anthropic billing at contract rates |
| 7 | `/login` credential | *from the plan* | — | `billingType` decides; see below |
| — | none of the above | `unknown` | — | |

**Mode and provider are deliberately orthogonal.** "Is this per-token" and "who
bills it" are different questions, so folding the vendor into the mode would force
a consumer to enumerate vendors to answer the first. Crucially `api` must keep
meaning *per-token at list price, so this figure IS spend* — a Bedrock session is
per-token at an AWS-negotiated rate, so calling it `api` would invite a consumer to
present the wrong number as spend. `metered_external` says "somebody else sets the
rate"; the provider says who. A new provider is a value rather than a new mode.

**Ranks 5 and 7 resolve from the account, not from the tier.** A plan-backed
credential proves plan-backed *auth*, not the plan's billing model: an enterprise
org can sit on usage-based billing (`seatTier: enterprise_usage_based`), and
`billingType: usage_based` is the Claude Console path for organizations that prefer
API-based billing. Both report `api`. The subscription family
(`stripe_subscription`, `stripe_subscription_contracted`, `apple_subscription`,
`google_play_subscription`) reports `subscription`; anything unrecognised reports
`unknown` rather than being assumed a subscription.

The config file is consulted **only at rank 7**, because it describes who the user
*is* rather than how this session bills. Reading it first is the bug this ordering
exists to prevent.

**Credentials are read for presence; the rank-1 flags are parsed as booleans.** An
API key's value is never read, so any non-empty string counts, and an empty
variable counts as unset since that is how shells leave one. The three
`CLAUDE_CODE_USE_*` selectors are different: they carry a boolean, and Claude Code
coerces each with `["1","true","yes","on"].includes(String(v).toLowerCase().trim())`,
so we match that set exactly. Both ways of getting it wrong mislabel a customer's
cost, in opposite directions:

- Counting any non-empty value reports `metered_external` for
  `CLAUDE_CODE_USE_BEDROCK=0`, telling a subscriber their figure is metered by AWS.
  Setting a flag to `0` is an ordinary way to turn it off, and the CLI honours it.
- Counting only `1` and `true` reports `subscription` for `=yes` or `=on`, telling a
  real Bedrock session that its list-price figure is a rationed allowance.

`settings.env` needs no special handling: Claude Code merges every settings scope's
`env` block into the process environment (managed last), and hooks inherit it. So a
key configured in user, project, or managed settings already reaches rank 2/3.

**Three credential forms are invisible to a hook and fall through:**

- **rank 4, `apiKeyHelper`** — a *command* that mints a key at runtime, so it never
  becomes a value we can observe. A user with a helper *and* a stale login
  credential is reported `subscription`. Detecting it would mean reading the
  settings precedence chain, two tiers of which (server-managed and CLI args) are
  unreadable from a hook, so a partial read would only create false confidence.
- **a Claude apps gateway session**, which outranks even rank 1 but exposes no
  documented environment signal.
- **rank 6's "active profile" form** — only the env-driven forms (a named
  `ANTHROPIC_PROFILE`, or the federation pair) are detected. An active profile is
  chosen by a file in the Anthropic config directory and its rank against `/login`
  depends on the auth mode recorded inside it, which is more depth than a cost
  annotation warrants, so it falls through to rank 7.

Config file resolution follows the CLI (undocumented, taken from the 2.1.81
bundle), and note the asymmetric defaults:

```
configDir = $CLAUDE_CONFIG_DIR ?? ~/.claude
1. $configDir/.config.json                    ← wins if it exists
2. ${CLAUDE_CONFIG_DIR:-$HOME}/.claude.json
```

`plan_type` comes from `claudeMaxTier` when it names a real tier, else `seatTier` —
an account can be `not_max` *and* `team_standard` at once, so keying on the Max
tier alone would report "not_max" for a paying Team customer.

**Codex — from the rollout's `rate_limits`,** covered below. Everything from here
to the end of this section is Codex-only; Claude Code persists no allowance data
locally.

**Allowance windows.** A plan enforces one or two windows at once, and you are
blocked when *either* exhausts. Codex models both slots as the same
`RateLimitWindow`, whose `window_minutes` it documents as "Rolling window
duration, in minutes".

**Which duration lands in which slot depends on the plan**, so read
`window_minutes` rather than assuming an ordering. Observed and inferred from
codex 0.142.5:

| Plan | `primary` | `secondary` |
|---|---|---|
| Free | monthly (`43200`) — observed | absent — observed |
| Paid | 5-hour (`300`) | weekly (`10080`) |

The paid row is inferred, not observed: the binary carries `five-hour-limit` and
`weekly-limit` status placeholders, and its reset-credit copy comes in exactly two
flavours — "Reset your current 5-hour and weekly usage limits" (a pair) versus
"Reset your current monthly usage limit" (alone).

A dashboard that assumes two windows renders empty for free-plan sessions, and one
that assumes `primary` is short mislabels them. A slot the plan omits is not
emitted.

The two windows answer different questions: a short window means blocked *now*,
recovering in hours; a long one means degraded for days. `reached_type` names which
window tripped, which is what separates "wait" from "upgrade" — that is why it is a
string rather than a boolean.

Two rules the readers hold to:

- **Codex never emits `api`; Claude Code may.** For Codex a plan is only reported
  for ChatGPT-authenticated sessions, so an absent plan is *consistent with*
  API-key auth without proving it — claiming `api` would assert the figure is real
  spend, the very error this exists to prevent, so absence stays `unknown`. Claude
  Code is different: the docs make `ANTHROPIC_API_KEY` definitive, so `api` there is
  proven rather than inferred. The asymmetry is deliberate, not an inconsistency.
- **Unreported values are omitted, not zeroed.** "0% of allowance consumed" and
  "balance $0.00" read as measurements; a CLI that never reported them has made
  no such claim. `billing_mode` is the exception and is stated even as `unknown`,
  because alongside a cost figure "we looked and could not tell" differs from
  "we never looked".

Claude Code emits it only on spans that carry token usage: the mode exists to say
what a cost figure means, so on a turn that reported no tokens it would annotate
nothing. Codex currently emits it unconditionally — an inconsistency tracked
separately.

The namespace is harness-neutral (`dash0.gen_ai.*`, not `dash0.codex.*`) because
the same mismatch exists for Claude Code (Max plans) and Copilot (per-seat, so
its cost figure is never spend). `dash0.codex.rollout.compressed` stays
Codex-scoped as a reader diagnostic.

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
| `dash0.gen_ai.tool.skill.source` | `model` | Skill tool. Constant here — the tool call *is* the model choosing. |
| `dash0.gen_ai.code.lines_added` | integer | Claude Code only — from the Edit/Write/MultiEdit `structuredPatch`. |
| `dash0.gen_ai.code.lines_removed` | integer | Claude Code only — from the Edit/Write/MultiEdit `structuredPatch`. |
| `dash0.gen_ai.vcs.pull_request.url` | PR / MR URL | Survives `omit_io`. |
| `dash0.gen_ai.vcs.issue.url` | Issue URL | Survives `omit_io`. |
| `dash0.gen_ai.vcs.commit.sha` | Commit SHA | Survives `omit_io`. |
| `exception.message` | Error text | On `PostToolUseFailure`. |
