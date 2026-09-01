# Development

## Releasing

**Actions → Release.** One button, and it is the whole thing: pick `patch`
(default), `minor` or `major`, and the workflow works out the next version,
writes it into every file that pins one, builds, verifies, publishes, and moves
`main` last.

There is no prepare step, no bump PR and no merge to wait for.

### Why the order is what it is

Everything happens while `main` still points at the *old* version. `main` learns
the new one last, once the binaries are published and proven downloadable:

1. Check out the commit `main` pointed at when the button was pressed.
2. Work out the version, write it everywhere, commit and tag — **locally**.
3. Build every binary `.goreleaser.yaml` describes — 24 today: four agents,
   three platforms, two architectures — and upload them to a **draft** release.
4. Verify: checksums, `dist/` matches that list by name, the Linux binary
   actually runs, and the uploaded assets match what was built.
5. Push the tag, then flip the draft to published.
6. Check every public download URL a bootstrap can build.
7. Push the bump to `main`, last.

Steps 1 to 4 publish nothing, and a failure in any of them deletes the draft.
Step 5 is where the release becomes real, step 6 proves it is downloadable, and
only then does `main` name it.

**`main` never names a version you cannot install.** That is the point of putting
step 7 last: the assets are public and verified before anything an install reads
mentions them. Before this, the gap between `main` naming a version and that
version existing was 57 seconds when a merge triggered the build, and unbounded
before that, when a human had to remember to push the tag.

**If someone merges mid-release**, step 7 is a plain `git push` and is rejected —
it is a compare-and-swap, and the App's ruleset bypass does not weaken that. The
release is already public at that point, so `main` is left pinning the previous
version. Do not delete the release; installs may already have taken it. Instead
open a PR running `./scripts/version.sh set <version>` and merge it.

The planner refuses to cut anything while `main` is behind a published release,
so this cannot be papered over by pressing Release again — which would count
from the published version, cut the next one, and skip it permanently.

**If a run fails before step 7**, `main` still pins the old version and nothing
downstream changed. The draft goes, and so does the tag if step 5 had got as far
as pushing it — a tag on a bump commit that never reached `main` would otherwise
refuse every later run. If it fails after the bump merged but before a release
exists, run Release again: the planner sees `main` carrying an unreleased bump
and finishes that one instead of starting another.

### Dry run

**`dry_run`** — build and check, publish nothing. No tag, no release; the binaries
are attached to the run for 7 days. Safe to run at any time.

`DASH0_VERSION` points an install at any published release without editing
anything — a rollback, or a build under test:

```bash
export DASH0_VERSION=0.1.24
```

The four POSIX bootstraps read it, and the cache filename embeds the version, so
it never collides with the pinned build. They validate it against the same shape
a release uses, because it reaches both a download URL and a filesystem path: a
value containing `..` retargets the download at another repository, and
`checksums.txt` comes from the same base, so verification would pass against the
attacker's own manifest.

Two gaps, neither closed here:

- **The Windows hooks ignore it.** `cursor-on-event.ps1`, `codex-on-event.ps1`
  and `copilot-on-event.ps1` always use their pinned `$Version`. Confusingly,
  `install-cursor.ps1` and `install-codex.ps1` *do* read it, so on Windows the
  variable is honoured at install time and ignored at event time.
- **The installers do not validate it.** `install-cursor.sh`,
  `install-codex.sh` and both `.ps1` installers read `DASH0_VERSION` into the
  same download URL and filesystem path with no check. The bootstrap guard is
  the second line of defence; by the time a hook runs, the installer has already
  written the binary.

> **No dev channel yet.** Cutting a prerelease from a feature branch and gating
> the App credential by branch are mutually exclusive without splitting the job
> graph: the environment holding the credential is restricted to `main`, so a job
> declaring it cannot run anywhere else. Adding it back means moving the push
> into its own job, which is a change worth making on its own.

### How it is wired

- **`scripts/version.sh`** — `check`, `set`, `latest`, `next`. The only list of
  the thirteen places a version is pinned, so the bump and the check cannot disagree
  about what needs bumping. `next` counts from the newest **published release**,
  not from tags or the manifests, both of which can name a version that was never
  released.
- **Pushing to `main`** uses the *Dash0 Release Bot* App, for that one command
  only. Everything else uses the built-in token. GitHub Actions' own token cannot
  be granted this — GitHub refuses it as a ruleset bypass actor with a 422 — so
  the App is named in the `Protect Main Branch` bypass list instead.
- **The App credentials live in a GitHub Environment restricted to `main`**, not
  in repo secrets. This is the control that matters: `workflow_dispatch` runs the
  workflow file *from the branch it is dispatched from*, so a check inside the
  tree is one an attacker also controls. A job declaring the environment on any
  other ref is rejected before it starts, and a job that drops the declaration
  finds nothing to read.
- **GoReleaser uploads into a draft.** It creates the release before uploading
  and writes `checksums.txt` last, so publishing at creation would leave a window
  where the tag resolves but a binary does not.
- **`concurrency: release`** serializes runs; `mode: replace` means two runs on
  one tag would delete each other's uploads.
- **After publishing**, `scripts/verify-release-assets.sh --strict` checks every
  asset name a bootstrap can ask for at its public URL, then the end-to-end job
  installs the real binary through `claude-on-event.sh`. CI runs the same script
  non-strict, where a 404 is a warning — the normal state of a PR.

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
| `gen_ai.usage.cache_creation.input_tokens` | integer                                                              | All four runtimes. Codex reports it as `cache_write_input_tokens`; every value observed so far is zero, but the field is on the wire and is emitted as it comes. |
| `dash0.gen_ai.usage.cache_creation.ephemeral_5m.input_tokens` | integer                                                              | Claude only. |
| `dash0.gen_ai.usage.cache_creation.ephemeral_1h.input_tokens` | integer                                                              | Claude only. |
| `gen_ai.usage.reasoning.output_tokens` | integer                                                              | Claude (from the transcript), Codex (from the rollout) and Copilot, all only when > 0. A subset of `output_tokens`, not an addition — cost is unaffected, and absence means the turn did no thinking. |
| `gen_ai.request.reasoning.level` | `low`, `medium`, `high`, `xhigh`                                     | Claude only, from the payload's `effort` field. The request-side counterpart to `reasoning.output_tokens`: the setting that produced the thinking those tokens paid for. The attribute is a free-form string — the convention asks for "the exact string value sent to the provider" and gives `low`/`medium`/`high` only as examples — so Claude Code's `xhigh` is reported as-is. |
| `dash0.gen_ai.tool.skill.name` | e.g. `writing:unslop`                                                | Claude and Codex, on the chat span of a turn that a person's slash command or `$mention` started, and for Codex on any turn that loaded a skill at all. See below. |
| `dash0.gen_ai.tool.skill.source` | `command`, `model`                                                   | Same rows as above. `model` reaches a chat span only on Codex, where the model's own choice also loads without a tool call. |
| `gen_ai.input.messages` | JSON: `[{"role":"user","parts":[{"type":"text","content":"…"}]}]`    | Content-gated by `omit_io`.    |
| `gen_ai.output.messages` | JSON: `[{"role":"assistant","parts":[{"type":"text","content":"…"}]}]` | Content-gated by `omit_io`.    |
| `gen_ai.agent.id` | Sub-agent ID                                                         | On `invoke_agent` spans. Per invocation, not per kind: on Copilot it is the `call_…` id of the `task` tool that spawned the sub-agent, which is also the session id Copilot gives that sub-agent's own hooks under `copilot -p` (interactively that session id is a plain UUID instead, so the join holds in prompt mode only). If the spawning tool's span flushed into a later turn than the agent's, no `invoke_agent` span is emitted for that sub-agent at all — see `copilot/README.md`. |
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

**Codex has the same two routes and neither is a tool call.** It loads a skill by injecting
it into the conversation — "progressive disclosure": the model sees every skill's name and
description, and the full `SKILL.md` arrives only once it picks one. So both routes land on
the turn's `chat` span, and the split comes from whether the person named the skill with
Codex's `$mention`: `command` when they did, `model` when the model chose from the
catalogue. There is no `execute_tool Skill` span on Codex at all, which is worth knowing
before comparing counts across runtimes.

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
| 1 | `CLAUDE_CODE_USE_BEDROCK` / `_FOUNDRY` / `_ANTHROPIC_AWS` / `_ANTHROPIC_GOOGLE_CLOUD` / `_MANTLE` / `_VERTEX`, in that order | `metered_external` | `bedrock` / `foundry` / `bedrock` / `vertex` / `bedrock` / `vertex` | AWS / Microsoft / Google bills, at a rate we cannot see |
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
API key's value is never read, so any non-empty string counts and an empty variable
counts as unset. The `CLAUDE_CODE_USE_*` selectors carry a boolean instead, and the
CLI coerces each with
`["1","true","yes","on"].includes(String(v).toLowerCase().trim())`, so we match that
set exactly. Both ways of getting it wrong mislabel a cost figure: counting any
non-empty value reports `metered_external` for `CLAUDE_CODE_USE_BEDROCK=0`, and
counting only `1`/`true` reports `subscription` for `=yes`.

The rank-1 order and the six selectors come from the CLI's provider resolver
(2.1.238), which is why `_FOUNDRY` precedes `_VERTEX`. Two AWS-family selectors
report `bedrock` and the Google one reports `vertex`: the provider says who meters
the session, and a finer vendor name would grow the value set without changing the
answer to that question.

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
| `gen_ai.request.model` | `claude-…`, `gpt-…`, … | The model of the actor that made the call. A tool call carrying `agent_id` is resolved from that sub-agent's own transcript, so it agrees with the `invoke_agent` span above it; omitted rather than filled from the session's model when that transcript is not on disk yet. |
| `gen_ai.request.reasoning.level` | `low`, `medium`, `high`, `xhigh` | Claude only. Same source and meaning as on the chat span. |
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
