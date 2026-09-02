---
qa_root: qa
app_kind: plugin
config_file: qa/config.local.json
# The last day every check of every runtime ran. 2026-09-01 added and ran the
# cursor arm and the shared checks, but did not re-run the claude-only, codex-only
# or copilot-only ones, so it is not a full pass. 2026-08-28 did the same for the
# copilot arm.
last_full_pass: 2026-08-25
---

# QA setup for dash0-agent-plugin

A QA run drives a real coding-agent session against the plugin, lets it export to
a real Dash0 target, and then reads the spans back with `dash0 spans query`.
Alongside the plugin's own hooks it registers a recorder that captures every hook
payload and a snapshot of the transcript as it stood at that moment. That
recording is the pipeline's entire input, so the expectation can be computed
without the plugin's involvement, and each recorded pair is a replayable
unit-test fixture.

The target is a shared, non-disposable dataset that a QA run does not own. So
nothing in a run may be destructive, and every read must filter by
`gen_ai.conversation.id`: an unfiltered query returns other sessions, and a
count taken without that filter is meaningless. QA spans cannot be deleted
afterwards either; they stay in the dataset. Treat anything a query returns that
is not this run's own session as none of QA's business, and keep it out of every
spec, learning, finding, and report.

## Runtimes

All four supported agents are covered here, **one spec tree per runtime**:
`qa/specs/claude/`, `qa/specs/codex/`, `qa/specs/copilot/` and
`qa/specs/cursor/`, each split by topic underneath. Each spec also names its
runtime in frontmatter, so the area and the field cannot drift apart. The split
is by runtime rather than by topic because a run is one driver, one credential
and one cost profile — `/qa-run codex` is a coherent thing to execute, while a
topic area spanning all four would need four drivers mid-run. A spec written for
one runtime says nothing about the others. They share the Go pipeline and
therefore share most invariants, but they differ in what a run can prove:

| | claude | codex | copilot | cursor |
| --- | --- | --- | --- | --- |
| Driver | `qa/tools/qa-session.sh` | `qa/tools/qa-session-codex.sh` | `qa/tools/qa-session-copilot.sh` | `qa/tools/qa-session-cursor.sh`, through a pty |
| What is under test | the plugin **as this machine has it installed** | the shipped install path, **provisioned into a throwaway home** | the shipped marketplace install, **provisioned into a throwaway home** | the machine's own registration, which must be the **shipped wrapper** |
| Who configures it | the managed install; QA cannot | QA, from `qa/config.local.json` | QA, from `qa/config.local.json` | QA, from `qa/config.local.json`, through `CURSOR_PLUGIN_OPTION_*` |
| Second channel | the transcript, via `claude-code-usage-audit.py` | the rollout, via `qa/tools/qa-rollout.py` (usage only) | the native-OTel file, via `qa/tools/qa-otel.py` (usage **and** tool spans) | the transcript, via `qa/tools/qa-transcript-cursor.py` (turns only; **no usage**) |
| Harness's own figures | `claude -p --output-format json`, including cost | `codex exec --json`; Codex reports no cost | `copilot --output-format json`; output tokens and AI credits, no input tokens | none. The TUI has no machine-readable output |
| Sees what was sent | no | yes, through the plugin's debug log | yes, through the plugin's debug log | yes, through the plugin's debug log |
| Session id | pinned with `--session-id` | discovered from the recording | pinned with `--session-id` | discovered from the recording |
| Touches the machine | yes: the binary cache, under `QA_SWAP_BINARY=1` | no | no | no. `DASH0_PLUGIN_DATA` moves the cache into the run |

The asymmetry is not a preference, it is what each host allows. Claude Code's
options arrive from a managed `remote-settings.json` that beats every override,
so QA has to take the install as it finds it. Codex and Copilot have no managed
layer at all, so QA provisions one and gets a hermetic run in exchange. Cursor
sits between the two and allows exactly one of the pair: its stored login does
not travel — a copied `cli-config.json` does not authenticate, and
`CURSOR_CONFIG_DIR` moves the configuration without moving where hooks are read
from — so the registration must be the machine's. But Cursor passes the
launcher's environment to hook processes, which Copilot does not, so QA can point
that registration at the QA target for the duration of the run and nothing else
changes.

**Copilot inverts the shape of the evidence, and a report read without knowing
that is wrong in both directions.** On the other two runtimes the hook recording
is the pipeline's whole input, so it alone implies every span. Copilot's hooks
carry no numbers and no tool events the plugin consumes: the plugin reads
Copilot's own OpenTelemetry file at each turn boundary, and every token, model
and tool span comes from there. Its cost figure stays in the file, deliberately. So

- the `hooks` column expects a `chat` span and says `-` for the rest;
- the tool and token comparison runs against the OTel file, which is *also the
  plugin's input*, so agreement proves a faithful copy and not a correct
  measurement;
- a Copilot run has exactly one fully independent record, the hook lifecycle,
  and it can only speak for turns.

**Cursor inverts a different thing, and it is the second channel rather than the
first.** Its hook recording is the pipeline's whole input, exactly as on Claude
and Codex, so it implies every span and the `hooks` column is a real expectation
for all three span types. What it lacks is a second reading of a token count.
Cursor exposes usage in one place, the `afterAgentResponse` payload, and that
payload is the plugin's input; the agent transcript carries no number of any
kind. So

- the token column reads `-` in both non-Dash0 columns, and nothing is compared
  against it;
- the transcript corroborates the **turn count**, from its `<user_query>`
  entries, and that is the whole of what it corroborates;
- its tool count is printed and never compared. It is a superset in another
  vocabulary: measured 2026-09-01, 15 `tool_use` blocks against 11 hooks,
  because Cursor collapses `Glob` and `Grep` into one hook name and records
  internal plumbing that fires no hook at all.

A cursor spec can therefore assert that a token count is *scoped* correctly —
turn 2 is not turn 1 plus turn 2 — and never that it is correct.

**What each runtime therefore cannot answer.** A `claude` run cannot see the
bytes the plugin sent, so questions about the wire belong in `test/e2e/`. A
`codex` or `copilot` run cannot tell you whether that CLI's install on this
machine is configured correctly, because it does not use it — a `cursor` run is
the opposite and says nothing about any install but this machine's. A `copilot`
run cannot independently confirm a token count, and a `cursor` run cannot either,
for the reasons above. No runtime's result carries over to another: a fix
verified on `claude` is unverified on `codex` until a `codex` spec says
otherwise, and the same for `copilot` and `cursor`.

`test/installers/` covers Cursor's installer and uninstaller, and
`test/consistency/` its shipped files. What no layer below QA reaches on this
runtime is a session: the bytes on the wire from a real turn.

`test/e2e/` excludes Cursor on authentication, not on telemetry. The interactive
TUI fires the full event set including usage, so the canary's own pty driver
could type into it. What it cannot do is run hermetically: a copied
`cli-config.json` does not authenticate, and `CURSOR_CONFIG_DIR` moves the
config directory without moving where hooks are read from. A canary would have
to write the machine's real `~/.cursor/hooks.json`, which is what the QA driver
does and what a test in CI must not. `cursor-agent -p` avoids none of that and
fires no turn hooks at all. The package doc in `test/e2e/main_test.go` records
this against a pinned CLI version.

`test/marketplaces/` excludes Cursor for an unrelated reason: `cursor-agent
plugin` has no install verb, and `add` takes a git URL with no local-source
variant.

## Layout

- specs:     qa/specs/<runtime>/<topic>/   (`claude/session`, `codex/subagents`, `copilot/turns`, `cursor/mcp`, ...)
- learnings: qa/learnings/
- findings:  qa/findings/        (open spec failures only; a fixed one is deleted)
- runs:      qa/runs/            (gitignored)
- fixtures:  qa/recorder/, qa/tools/
- config:    qa/config.local.json (gitignored), qa/config.local.json.example

## Configure

`qa/config.local.json` holds the API endpoint, the tokens, and the dataset. Copy
the example and fill it in. Ask the team for the values; they are the same ones
the `dash0` repository's QA setup uses. Never invent a token, a URL, or a tenant
name, and never lift one out of shell history or an earlier transcript.

```sh
cp qa/config.local.json.example qa/config.local.json
chmod 600 qa/config.local.json
```

| Key | Meaning | Sharp edge |
| --- | --- | --- |
| `apiUrl` | Where spans are read from | Must be the API host, not the ingress host. The two differ only in a hostname prefix, and pointing at the wrong one fails as a connection error rather than an auth error. |
| `appUrl` | UI base for a session link | Only used to build a human link. `internal/sessionurl/sessionurl.go` derives the same value from the ingress host, so a mismatch here means a report links somewhere the spans are not. |
| `ingestUrl` | Where the plugin is expected to write | For `claude`, nothing is sent here: it exists so a check can prove QA reads the environment the plugin writes to. For `codex`, `copilot` and `cursor`, this is where the install actually exports. |
| `authToken` | Reads spans back, and for `codex`, `copilot` and `cursor` also ingests them | A live token, and it must do **both**. The `claude` runtime only reads, so a read-scoped token is enough there. The other two provision the install and hand this same token to the plugin, and a token that cannot ingest 401s on every export — a run that looks perfectly healthy and reports zero spans. `ingest-token-reaches-the-ingress` proves it before a session is paid for. The two permissions really are separate, so reading is no guarantee of ingesting: measured 2026-08-26, an ingest-scoped token answers a query with `403 ... *:read permission is required`, and measured 2026-08-28, an access token copied out of the `dash0` CLI's own profile gets `401` from the ingress. Ask the team for the QA token rather than improvising one from a CLI profile. |
| `dataset` | The dataset to read, and for `codex`, `copilot` and `cursor` to write | Must be the installed plugin's `DATASET`, which is `default`, not `qa`. Reading a *different readable* dataset returns an empty result that looks exactly like the plugin having sent nothing. |
| `org` | Organization slug | Informational. |

The `dash0` CLI's own active profile is deliberately not used. It carries its own
dataset, which on this machine resolves to one the token cannot read, and every
QA command therefore passes `--api-url`, `--auth-token`, and `--dataset`
explicitly.

> [!CAUTION]
> `authToken` is live, against a shared environment, and on the `codex`,
> `copilot` and `cursor` runtimes it can write as well as read. It never goes into a ticket, a
> message, a commit, or a screenshot. `qa-compare.py` strips it from any command
> it prints, and `run-dir-carries-no-real-credential` checks that no run directory
> picked it up.

> [!TIP]
> **Do not improvise the token out of the `dash0` CLI's profile.** It is tempting,
> because it is right there and it reads the dataset. It is a short-lived OAuth
> access token that expires within the hour, and it cannot ingest at all, so a
> `codex`, `copilot` or `cursor` run configured with it 401s on every export and
> reports zero spans. Both were measured on 2026-08-28. The symptom of the first is
> `qa-compare.py` and `qa-attrs.py` exiting 2 with `401 invalid or expired OAuth
> access token` in the middle of an otherwise green run; the symptom of the second
> is a healthy session with nothing in Dash0. Ask the team for the QA token.

### Claude Code

**The thing under test is not configured by QA.** The installed plugin runs with
its own configuration, which on a Dash0 machine comes from
`~/.claude/remote-settings.json` under
`pluginConfigs/dash0-agent-plugin@dash0/options` and arrives as
`CLAUDE_PLUGIN_OPTION_*`. `qa/tools/qa-session.sh` adds one thing to the session:
a second hook handler, registered in a scratch project's `.claude/settings.json`,
generated from `claude/hooks.json` so it cannot miss an event the plugin acts on.

**What is configured is the read side**, and only the read side. The comparison
reads back through `apiUrl`, `authToken`, and `dataset`; nothing in a `claude` run
tells the plugin anything.

Authoritative request shape: `claude/hooks.json` for the event list,
`DEVELOPMENT.md` for the span and attribute contract, `internal/otlp/otlp.go` for
the wire format. Worked example payloads now come from the runs themselves:
`qa/runs/<id>/record/events/*.json` are real payloads, paired with the exact
transcript bytes the pipeline would have read.

> [!CAUTION]
> **Never write a `dash0-agent-plugin.local.md` into a QA project.** The wrapper's
> `load_settings` reads a project-level config file for *every* registration in
> the session, and its `auth_token` becomes `CLAUDE_PLUGIN_OPTION_AUTH_TOKEN` for
> the installed plugin too. An earlier version of this harness did exactly that:
> the installed plugin kept its real endpoint, received a QA token, and got a 401
> on every export. Six probe sessions produced zero spans in Dash0 and the run
> read as "the plugin sends nothing". `no-project-config-overrides-the-install`
> is the check for it.

**What cannot be reconfigured, and what that costs.** The options
`~/.claude/remote-settings.json` supplies beat every `DASH0_*` value, and neither
`env -u` nor `--settings` overrides them — both were tried. So a run cannot
choose the dataset, cannot turn on `omit_io`, and cannot turn on the plugin's
debug payload log. There is therefore **no transport-level channel**: a question
about the exact bytes on the wire cannot be answered without reconfiguring the
install, and reconfiguring it is what the caution above forbids. Answer those
questions in `test/e2e/` instead, which owns the wire format against a mock.

#### Testing an unreleased change

By default a run tests the installed release, because that is what the machine
actually runs. `QA_SWAP_BINARY=1` builds the working tree over the installed
binary cache for the duration of the run and restores it on exit, including on
failure. It is opt-in because that cache is shared with the developer's own live
sessions.

### Codex

**QA provisions the thing under test, into a throwaway home.** There is no
managed Codex configuration to defer to, and mutating the developer's `~/.codex`
would register the QA recorder for their own live sessions. So
`qa/tools/qa-session-codex.sh` creates a `mktemp -d` home, runs the shipped
`install-codex.sh` into it with `HOME` and `XDG_STATE_HOME` pointed there, and
deletes it afterwards. Nothing outside that directory is written, so unlike the
`claude` runtime there is nothing to restore and no shared cache to disturb.

What that buys, and what it costs, is in `## Runtimes` above. The short version:
the run tests the shipped install path against a real Dash0 target, and says
nothing about the install on this machine.

Registration order is load-bearing. Codex enforces hook trust, and the trust key
is `<resolved config path>:<event>:<group index>:<handler index>`, so:

1. `qa/tools/qa-codex-hooks` writes the recorder's block first, into an empty
   `config.toml`, claiming group index 0. It walks `codex.HookEvents` and calls
   `codex.TrustHash`, so the recorder covers exactly the events the plugin acts
   on and is trusted by exactly the rule the product reproduces.
2. `install-codex.sh` appends the plugin's block, which counts the existing group
   and correctly takes index 1.

Reverse those and both blocks claim index 0, one of them is untrusted, and Codex
skips it **in silence** — no prompt, no log line, no error. The observed symptom
is a healthy session with an empty recording, which is why
`codex-recorder-is-trusted-and-the-plugin-keeps-its-index` is blocking and why it
is worth its runtime. Two further traps in the same area: the trust key embeds
the *resolved* config path, so a config.toml that is copied rather than
regenerated is untrusted at its new path; and `codex.StripManagedBlock` removes
anything between the plugin's markers before counting groups, so the recorder's
block must not wear those markers.

> [!CAUTION]
> **Never write a `.codex/dash0-agent-plugin.local.md` into a QA project.** The
> bootstrap prefers a project-level config over the global one, so it would
> silently retarget the very install the run just provisioned. This is the same
> trap the `claude` runtime has, reached by a different door.

**Auth.** `OPENAI_API_KEY` is used when set, through `codex login --with-api-key`
into the throwaway home. That is the clean path: an API key is not refreshed, so
nothing about the machine's own login can be affected.

> [!WARNING]
> `QA_CODEX_REUSE_LOGIN=1` symlinks `~/.codex/auth.json` instead. It is symlinked
> rather than copied so no live credential is duplicated onto disk, but Codex
> **refreshes** that file, and a refresh that lands as an atomic rename replaces
> the symlink and leaves the machine's real login holding a rotated-away refresh
> token. The blast radius is the developer's `codex login`, not the plugin. It is
> opt-in for that reason; prefer a key.

**Knobs.** `QA_MODEL`, `QA_CODEX_BINARY=working-tree` (build the working tree
instead of installing the release — safe by default here, since the binary goes
into the throwaway home), `QA_CODEX_SANDBOX`, `QA_KEEP_SCRATCH=1` (keep the
throwaway home, which holds the ingest token, for debugging),
`QA_CODEX_BYPASS_TRUST=1` (see below), and two that unlock behaviour a plain
`codex exec` cannot reach:

- `QA_CODEX_RESUME="<second prompt>"` drives a second turn into the same session
  through `codex exec resume --last`. One exec session is one turn, so without it
  "this turn's usage" and "the session's usage" are the same number and a
  per-turn bug is invisible.
- `QA_CODEX_MULTI_AGENT=1` adds `--enable multi_agent_mode`. Sub-agents are off
  by default in 0.149.1, so without it a prompt asking the model to delegate is
  simply answered directly.

`QA_CODEX_BYPASS_TRUST=1` passes `--dangerously-bypass-hook-trust`. It exists to
isolate one failure: if a run records nothing and recording appears with this
flag, the reproduced hashes in `internal/source/codex/trust.go` are stale against
the installed Codex. A run that needed it proves nothing about trust, the
manifest records `trust_bypassed`, and the report must say so.

Authoritative shapes: `codex/hooks.json` and `codex.HookEvents` in
`internal/source/codex/trust.go` for the event list — the two are separate lists
and `test/consistency` fails when they diverge — `internal/source/codex/codex.go`
for how a payload is normalized, `internal/source/codex/rollout.go` for where
usage comes from, and `DEVELOPMENT.md` for the attribute contract.

### GitHub Copilot CLI

**QA provisions the thing under test, into a throwaway home**, for the same
reasons as Codex: there is no managed Copilot configuration to defer to, and
registering the QA recorder in the developer's `~/.copilot` would attach it to
their own live sessions. `qa/tools/qa-session-copilot.sh` creates a `mktemp -d`
home, points `HOME` and `COPILOT_HOME` at it, and installs the plugin the way a
user does:

```
copilot plugin marketplace add <repo root>
copilot plugin install dash0-agent-plugin@dash0
```

That is the repository's real `.github/plugin/marketplace.json` and the real
`copilot/` package, so the manifest, the camelCase `hooks.json`,
`${PLUGIN_ROOT}` resolution and the bootstrap are all the shipped ones. By
default the bootstrap then downloads the release binary;
`QA_COPILOT_BINARY=working-tree` pre-places a locally built one where it looks.
Nothing outside the throwaway home is written, so there is nothing to restore.

**Registration needs no ordering care.** Copilot has no hook-trust mechanism, so
unlike Codex there is no index to get right and no silent skip to guard against:
the recorder goes into `$COPILOT_HOME/hooks/qa-recorder.json` at user scope and
the plugin's hooks are simply additive.

The recorder's lifecycle events are generated from `copilot/hooks.json`, so it
cannot miss an event the plugin acts on. It also registers `postToolUse`,
`postToolUseFailure`, `subagentStart` and `subagentStop`, which the plugin
deliberately ignores — that makes them a second opinion QA gets for free, and
`qa-compare.py` prints them as such rather than as an expectation. `preToolUse`
is never registered: it is Copilot's only fail-closed event, so a hook that
stumbles there blocks the session's tools.

**Native OTel is where every number comes from, and the driver is the launcher.**
The plugin cannot enable it — Copilot passes no launch environment to hook
processes — so in production the `dash0-configure` skill installs a shell
function that exports `COPILOT_OTEL_ENABLED`,
`COPILOT_OTEL_FILE_EXPORTER_PATH` and
`OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT` before running the real
`copilot`. The driver exports the same three. The path is **not** communicated
to the plugin: `internal/source/copilot.OtelDir()` resolves a fixed path under
`$HOME`, and the two sides can only agree on a baked-in convention. The driver
therefore writes into that convention path under the throwaway `HOME` rather
than setting `DASH0_COPILOT_OTEL_DIR`, so a run exercises the agreement instead
of papering over it.

Two deliberate differences from the launch function:

- **The file is kept**, not deleted at exit. It is the run's second channel, and
  a run directory that threw its evidence away cannot be re-read.
- **Both turns of a resumed run share one file.** The launch function gives each
  launch its own, which is the easy case; a fixed
  `COPILOT_OTEL_FILE_EXPORTER_PATH` is the documented alternative to that
  function and gives one file for both. The shared file is the harder case, and
  it is what caught the cursor defect below, so it is what the driver does.

> [!CAUTION]
> **Never write a `.copilot/dash0-agent-plugin.local.md` into a QA project.** The
> bootstrap prefers a project-level config over the global one for *every*
> registration in the session, so it would silently retarget the install the run
> just provisioned. This is the same trap the other two runtimes have, reached by
> a third door. The driver writes its config into the throwaway `HOME` only, and
> `copilot-driver-writes-no-project-config` is the check for it.

**Auth.** `COPILOT_GITHUB_TOKEN`, `GH_TOKEN` or `GITHUB_TOKEN`, in Copilot's own
order of precedence; the driver falls back to `gh auth token`. The throwaway home
carries no stored login, so without one of these the session fails at auth. A
`gh auth token` from a `gh auth login` works — verified 2026-08-28 against
Copilot CLI 1.0.80 with a token scoped `gist, read:org, repo`.

**Knobs.** `QA_MODEL`, `QA_COPILOT_BINARY=working-tree`, `QA_KEEP_SCRATCH=1`
(keeps the throwaway home, which holds two live tokens),
`QA_COPILOT_SKILL=1` (installs the `qa-echo` fixture into
`$COPILOT_HOME/skills/`), and two that unlock behaviour a single plain run
cannot reach:

- `QA_COPILOT_RESUME="<second prompt>"` drives a second turn into the same
  session through `copilot --resume=<id>`. One prompt-mode invocation is one
  turn, so without it "this turn's usage" and "the session's usage" are the same
  number and a per-turn bug is invisible. This is exactly how the cursor defect
  was found.
- `QA_COPILOT_NO_OTEL=1` runs with native OTel off. That is the plugin's
  documented degradation — a chat span per turn, no usage, no tool spans — and a
  spec asserting it needs this. Nothing else should use it: with no file, the
  second channel reports nothing and the run rests on the hook record alone.

Authoritative shapes: `copilot/hooks.json` for the event list,
`internal/source/copilot/copilot.go` for how a payload is normalized,
`internal/source/copilot/otelfile.go` for how a turn is recovered from the OTel
file, `copilot/README.md` for the sub-agent and tool-span design, and
`DEVELOPMENT.md` for the attribute contract.

### Cursor

**QA does not provision the thing under test, and it does not take it as it finds
it either.** Cursor allows exactly one of the two moves the other runtimes make.

*Why not a throwaway home.* Cursor's stored login does not travel. A copied
`cli-config.json` does not authenticate — measured 2026-09-01, a throwaway home
with that file still failed with `Authentication required` — and
`CURSOR_CONFIG_DIR`, which does move `hooks.json`'s directory according to the
CLI's own path resolution, turned out not to move where hooks are actually read
from: a session started fine against a redirected config directory and fired not
one hook from it. So the registration is the machine's `~/.cursor/hooks.json`,
under the real `HOME`, exactly as on the `claude` runtime.

*Why QA can still configure it.* Cursor passes the launcher's environment to hook
processes, which Copilot does not, and `internal/harness` ranks
`CURSOR_PLUGIN_OPTION_*` above the configuration file. So the driver exports the
QA endpoint, token, dataset, team name and debug log for the duration of the
session, and the developer's own `~/.cursor/dash0-agent-plugin.local.md` loses on
every key that matters. That is not a nicety: on the machine this was built on the
install exports to production while `qa/config.local.json` reads development, so
without the override the run is a healthy session with no spans to find.

*What it leaves alone.* `DASH0_PLUGIN_DATA` points into the run directory, so both
the bootstrap's binary cache and the plugin's session state live there and the
shared cache under `~/.local/state` is never written — the developer's live
sessions keep the binary they had. The recorder goes into
`$PROJECT/.cursor/hooks.json`, at project scope, so `~/.cursor/hooks.json` is not
edited either.

> [!CAUTION]
> **The registered wrapper must be the one the checkout ships, and the driver
> refuses otherwise.** A wrapper from before 0.1.25 read the configuration file
> itself and re-exported every value, including
> `export CURSOR_PLUGIN_OPTION_AUTH_TOKEN="$val"` — the same high-precedence form
> the driver uses. So it overwrites the QA token with the developer's own and every
> export 401s while the session runs perfectly. Measured 2026-09-01 against the
> v0.1.19 wrapper: 6 hooks recorded, both spans built and logged, zero spans in
> Dash0, and the report read as total telemetry loss. Configuration moved into Go
> after 0.1.24, so the shipped wrapper exports nothing.
>
> The driver compares the registered file against `cursor/cursor-on-event.sh` byte
> for byte rather than checking the `VERSION=` string, so a hand-edited wrapper at
> the right version is caught too. The remedy is a re-install.
> `QA_CURSOR_ALLOW_STALE=1` overrides it for the one case where testing the old
> install *is* the question; the manifest records `wrapper_matches_shipped: false`
> and `qa-compare.py` warns above the counts.

> [!CAUTION]
> **Never write a `.cursor/dash0-agent-plugin.local.md` into a QA project.** The
> same trap the other three runtimes have, reached by a fourth door: the config
> lookup prefers the working directory's copy over the one in `$HOME`, so a
> project-level file would outrank the QA environment for `enabled` and for
> anything the driver does not override. `cursor-driver-writes-no-project-config`
> is the check for it.

**QA cannot register its own wrapper instead**, and this is why the caution above
is a refusal rather than a workaround. Cursor honours the user-scope and the
project-scope hook file **together** — measured 2026-09-01, a project file's hooks
fired and the machine's kept firing — so a second registration of the plugin would
emit every span twice.

**Two ways the user's own configuration still wins, and the driver stops on
both.** `auth_token_keychain_service` in `~/.cursor/dash0-agent-plugin.local.md`
outranks `CURSOR_PLUGIN_OPTION_AUTH_TOKEN` by design, so the session would export
with that token instead of the QA one. And `enabled: false` returns before the
plugin reads anything, so the session emits nothing at all. Neither is a product
defect and neither is fixable from inside a run, so the driver refuses rather than
reporting a telemetry loss it caused itself.

**The driver types into a terminal.** `cursor-agent -p` fires neither
`beforeSubmitPrompt` nor `afterAgentResponse`, and `afterAgentResponse` is the
only event carrying usage and the only one that becomes a `chat` span. See
`### Cursor` under `## Stimulate` for the mechanics, and
[learnings/cursor-print-mode-fires-no-turn-hooks.md](learnings/cursor-print-mode-fires-no-turn-hooks.md)
for the measurement.

**Auth.** The machine's own `cursor-agent login`. Nothing is passed in and nothing
is written, so unlike `QA_CODEX_REUSE_LOGIN` a run cannot rotate or invalidate it.

**Knobs.** `QA_MODEL`, `QA_CURSOR_BINARY=working-tree` (build the working tree
over the run's own cache instead of letting the bootstrap download the release —
worth defaulting to, since a developer machine's install lags the checkout),
`QA_CURSOR_RESUME="<second prompt>"` (a second turn typed into the session that is
already open — no resume flag and no relaunch, which is a real advantage over the
other two multi-turn runtimes), `QA_CURSOR_MCP=1` (register the `qa/mcp-fixture/`
stub at project scope), and `QA_CURSOR_ALLOW_STALE=1` (see above).

Authoritative shapes: `cursor/hooks.json` for the event list,
`internal/source/cursor/cursor.go` for how a payload is normalized and which
events are dropped, `cursor/README.md` for the design, and `DEVELOPMENT.md` for
the attribute contract.

## Stimulate

### Claude Code

```sh
qa/tools/qa-session.sh "<prompt>" [run-id]
qa/tools/qa-compare.py qa/runs/<run-id>
qa/tools/qa-attrs.py qa/runs/<run-id>     # attribute surface, not counts
```

Knobs: `QA_MODEL` (`haiku` for probes), `QA_ALLOWED_TOOLS`, `QA_SWAP_BINARY`.
`qa-compare.py --dataset` overrides the dataset from the config for one run.

The independent record is `qa/runs/<id>/record/`:

| Artifact | Holds |
| --- | --- |
| `index.jsonl` | One line per hook invocation: event name, session, cwd, and the digests below, in wall-clock order |
| `events/<ns>-<Event>.json` | The stdin payload, byte for byte |
| `transcripts/<sha256>.jsonl` | The transcript at that invocation, content-addressed so an unchanged file costs one copy |

Nothing in the plugin writes any of it. `qa-compare.py` turns it into an
expectation using the hook-to-span mapping from `internal/pipeline/pipeline.go`:
`PostToolUse` and `PostToolUseFailure` each imply one `execute_tool`, `Stop` and
`StopFailure` one `chat`, `SubagentStop` one `invoke_agent`. Two further
independent figures come from `claude-code-usage-audit.py` over the final
transcript, and from `claude-result.json`.

A hook whose `transcript_path` does not exist yet is recorded as
`transcript_absent`, not as an error. Claude Code names the transcript before it
writes it, so `SessionStart`, `InstructionsLoaded`, and `UserPromptSubmit`
legitimately point at nothing, and `internal/transcript` sees the same absence.

> [!WARNING]
> The recorder and the plugin run as separate processes for the same event, so
> their transcript reads are not guaranteed to be the same bytes. A snapshot is
> the transcript within milliseconds of the plugin's own read, not provably the
> identical read. For a token count that has to be exact, use the final
> transcript, which both saw completely.

### Codex

```sh
qa/tools/qa-session-codex.sh "<prompt>" [run-id]
qa/tools/qa-compare.py qa/runs/<run-id>    # reads runtime from manifest.json
qa/tools/qa-attrs.py qa/runs/<run-id>      # runtime-agnostic; see the note below
qa/tools/qa-rollout.py qa/runs/<run-id>/rollout.jsonl   # the usage channel alone
```

The recorder is the same binary and needs no Codex-specific handling. Codex
reuses Claude Code's event names and payload field names — `hook_event_name`,
`session_id`, `cwd`, `transcript_path`, `agent_transcript_path` — so
`record/index.jsonl` and `record/events/` have the same shape, and
`record/transcripts/` holds Codex rollouts instead of Claude transcripts. The
hook-to-span mapping is shared too, so `qa-compare.py` computes the expectation
the same way.

Four artifacts are specific to this runtime:

| Artifact | Holds | Read it as |
| --- | --- | --- |
| `rollout.jsonl` | the final rollout, which no per-hook snapshot is | an independent record |
| `codex-events.jsonl` | Codex's own `--json` event stream | the harness's own figures |
| `plugin-debug.log` | every span the plugin emitted, as it emitted it | the product's output |
| `install.log` | what `install-codex.sh` did | provenance for the run |

Codex has no `--session-id` flag, so the session id cannot be pinned in advance.
The driver takes it from the recording, because the id the plugin was handed is
the id `gen_ai.conversation.id` carries. A run whose recording is empty therefore
has no session id at all, and the driver stops there rather than writing a
manifest nothing can verify.

> [!WARNING]
> A rollout can be `.zst`. Neither the plugin nor `qa-rollout.py` reads zstd, so
> usage is *unavailable* from such a run rather than zero; the plugin marks the
> span `dash0.codex.rollout.compressed` so the gap is visible in telemetry.
> Codex 0.149.1 writes plain `.jsonl`, so this has not been seen in the field.

`qa-attrs.py` needs no runtime switch. It reads `session_id`, `started_at` and
`ended_at` from the manifest and asks Dash0, all of which a Codex manifest
carries, and the attribute contract in `DEVELOPMENT.md` is one document covering
every runtime. The Codex-only keys are in it: `dash0.gen_ai.billing_mode`,
`plan_type`, the `rate_limit.*` family, the `credits.*` family, and
`dash0.codex.rollout.compressed`. Two of those families are assembled from a
prefix at runtime rather than written as literals, so `plugin_writes` will not
match them and they land in the informational "added at ingest" list. That is the
tool's documented floor, not a Codex problem — but on a `codex` run it is louder,
so read that list before quoting it. **None of this has been run against a Codex
session**, only read; the first `codex` probe is what confirms it.

### GitHub Copilot CLI

```sh
qa/tools/qa-session-copilot.sh "<prompt>" [run-id]
qa/tools/qa-compare.py qa/runs/<run-id>    # reads runtime from manifest.json
qa/tools/qa-attrs.py qa/runs/<run-id>      # runtime-agnostic
qa/tools/qa-otel.py qa/runs/<run-id>       # the native-OTel channel alone
```

The recorder is the same binary, with two small concessions to Copilot made in
`qa/recorder/main.go`. Copilot's camelCase payloads carry **no event-name field**
— the host passes it as an argv, exactly as the plugin's own bootstrap receives
it — and they name the session `sessionId` rather than `session_id`. The recorder
takes the argv when the payload is silent and reads either spelling, so
`record/index.jsonl` keeps one shape across all three runtimes and every consumer
can still filter on `session_id`. A Copilot payload's `transcriptPath` points at
Copilot's own `events.jsonl` rather than a Claude transcript; the plugin drops it
and the recorder snapshots it anyway, which costs one content-addressed copy and
gives the run the same per-hook "what the session looked like then" record.

Five artifacts are specific to this runtime:

| Artifact | Holds | Read it as |
| --- | --- | --- |
| `otel.jsonl` | Copilot's native-OTel file for the run | the second channel **and** the plugin's input |
| `otel-<n>.jsonl` | any further file the session produced | same, when a launch rotated the file |
| `copilot-events.jsonl` | Copilot's own `--output-format json` stream | the harness's own figures |
| `plugin-debug.log` | every span the plugin emitted, as it emitted it | the product's output |
| `install.log` | what the marketplace install did | provenance for the run |

Copilot **does** accept `--session-id` for a new session, so the driver pins one
rather than discovering it. Three records of that id then exist — the pinned
value, the recorder's, and Copilot's own `result` event — and the driver warns
when they disagree while `qa-compare.py` treats a disagreement as a finding.

Two shapes in the recording are normal on this runtime and read as failures if
you do not expect them:

- **`call_<toolCallId>` sessions.** A sub-agent fires its own hook lifecycle
  under a synthetic session id that carries nothing linking back to the parent
  conversation. The plugin drops those wholesale rather than mint a token-less
  conversation per sub-agent. `qa-compare.py` counts them separately and says so;
  they are not a reused run id.
- **A tool table where Dash0 has more than `postToolUse` fired.** Sub-agent tool
  calls fire no hook at all. They reach Dash0 through the OTel file, under the
  sub-agent's own `invoke_agent` span, which sits under the `task` span that
  spawned it.

`qa-attrs.py` needs no runtime switch here either, and after the first probe
there is no Copilot-only key left to know about: a sub-agent's identity rides on
the standard `gen_ai.agent.name` and `gen_ai.agent.id` of its own `invoke_agent`
span, exactly as on the other two runtimes.

The probe found a second such key, `github.copilot.cost`, and that one was
resolved the other way: the export was removed rather than documented. It is
Copilot's own accounting in **AI credits**, and it would have sat one attribute
away from `dash0.gen_ai.usage.cost`, which Dash0 derives from tokens at ingest
and reports in money. The credits figure is still in the native-OTel file, and
`qa-otel.py` and `qa-compare.py` both print it as a channel-two figure — so
seeing "cost 2.0 AI credits" in a report is correct, and seeing
`github.copilot.cost` on a *span* is a regression.

### Cursor

```sh
qa/tools/qa-session-cursor.sh "<prompt>" [run-id]
qa/tools/qa-compare.py qa/runs/<run-id>          # reads runtime from manifest.json
qa/tools/qa-attrs.py qa/runs/<run-id>            # runtime-agnostic
qa/tools/qa-transcript-cursor.py qa/runs/<run-id>   # the transcript channel alone
```

The recorder is the same binary and needs no Cursor-specific handling at all.
Cursor uses Claude Code's payload field names — `hook_event_name`, `session_id`,
`cwd`, `transcript_path` — so `record/index.jsonl` and `record/events/` have the
same shape, and `record/transcripts/` holds Cursor agent transcripts. The
hook-to-span mapping is Cursor's own lowerCamel spelling of the shared one:

| recorded event | normalized to | implies |
| --- | --- | --- |
| `afterAgentResponse` | `Stop` | one `chat` |
| `postToolUse` | `PostToolUse` | one `execute_tool` |
| `postToolUseFailure` | `PostToolUseFailure` | one `execute_tool`, ERROR |
| `subagentStop` | `SubagentStop` | one `invoke_agent` |

`sessionStart`, `sessionEnd` and `preToolUse` imply no span, and
`internal/source/cursor` drops `subagentStart` and the specialized
`before*`/`after*` helpers outright.

**The driver is a pty driver, and that is the whole reason this runtime works.**
`qa/tools/qa-cursor-drive.py` runs `cursor-agent --force --trust` under
`pty.fork()`, types each prompt, and ends with Ctrl-D. Four measurements are baked
into it, each of which cost a run:

- `--trust` is required, or the first paint is a trust dialog and nothing happens.
- The prompt text and its newline are two writes with a pause between them. One
  write is treated as a paste and left in the composer.
- **Turn completion is read from the recorder's index**, not from the screen. The
  TUI repaints constantly and every screen-scraping form of this was flaky; one
  `afterAgentResponse` row per completed turn is exact, and it cannot claim a turn
  finished before the evidence for it exists.
- **Ctrl-D is the only exit that fires `sessionEnd`.** `/quit` and two Ctrl-Cs
  both leave the process running until it is killed, and a killed session delivers
  no `sessionEnd`, so the pipeline never closes it.

**A failed drive is evidence, and it is never a result.** The driver exits `2`
when no `sessionStart` arrived, and `1` when a turn produced no
`afterAgentResponse` or the session ended without `sessionEnd`. The run directory
is written either way — `tty.log` and `record/` are how such a run gets diagnosed
— and then `qa-session-cursor.sh` exits with that code, because the session is
not the stimulus the spec asked for. `qa-compare.py` repeats it as a finding, and
also compares Dash0's `chat` count against the manifest's requested `turns`. That
last comparison is the only one that catches a partial run: a two-turn session
that died after the first turn has one chat span, one `afterAgentResponse` and one
prompt in the transcript, so all three columns agree with each other.

Four artifacts are specific to this runtime:

| Artifact | Holds | Read it as |
| --- | --- | --- |
| `transcript.jsonl` | Cursor's own agent transcript for the session | an independent record of turns; **not** of tools or tokens |
| `tty.log` | the terminal, with escape sequences stripped | what a person would have seen |
| `plugin-debug.log` | every span the plugin emitted, as it emitted it | the product's output |
| `plugin-data/` | the run's own binary cache and session state | provenance, and proof the shared cache was untouched |

Cursor has no `--session-id` flag, so the session id is discovered from the
recording. **A delegating run records more than one id, and that is normal.** The
main session is the one that fired `sessionStart`; a Cursor sub-agent runs under
its own freshly minted UUID, fires only `preToolUse` and `postToolUse`, and
carries no field linking it to the parent. The driver lists those in the manifest
as `subagent_sessions` and `qa-compare.py` reads that list, because Cursor's
sub-agent id — unlike Copilot's `call_` prefix — is indistinguishable from a real
session and every delegating run otherwise reported "the run id was reused".

Discovery reads only the rows this run appended. `record/index.jsonl` is never
deleted — a spec may have asked for that evidence — so the driver takes the row
count just before it launches `cursor-agent` and passes it as `--index-baseline`.
Everything downstream is scoped past it: the driver waits for *this* run's
`sessionStart` and counts *this* run's `afterAgentResponse`, and the manifest's
`hooks_recorded` is this run's figure. Without it, a rerun under a fixed run id —
which every setup check and every spec here uses — would satisfy the driver from
leftover rows, type into a terminal that is not up, and exit 0 on a session it
never drove.

`qa-attrs.py` needs no runtime switch here either. The first run found five raw
payload fields reaching every span — `conversation_id`, `generation_id`,
`cursor_version`, `workspace_roots`, and `failure_type` on the failure path only —
all now denied in `attrSkipKeys`. There is no Cursor-only documented key: an MCP
call's server rides on the standard `dash0.gen_ai.tool.mcp_server`, with the
literal placeholder `cursor` as its value.

## Observe

1. **Dash0** — `dash0 spans query` with the endpoint, token, and dataset from
   `qa/config.local.json`, filtered to `gen_ai.conversation.id`. This is the product's output
   as a consumer sees it, at full precision. `--precision disabled` is mandatory:
   adaptive sampling drops spans, and a dropped span reads as a span the plugin
   never sent. JSON output is capped at 100 records, so `qa-compare.py` warns when
   a result hits its limit rather than reporting a floor as a total.
2. **The harness's own figures** — for `claude`, `claude-result.json`: good at
   cost, which no span carries, and bad at sub-agents, because it reports the
   main session's usage only, so a session with a sub-agent shows numbers far
   below both Dash0 and the transcript. That gap is expected and is not a
   finding. For `codex`, `codex-events.jsonl`: no cost at all, and its event
   shape is Codex's to change, so `qa-compare.py` looks for usage and reports it
   as absent when it finds none, never as zero. For `copilot`,
   `copilot-events.jsonl`: per-message **output** tokens, an AI-credit figure and
   the session result, and no input tokens anywhere — those cells read `-`, and a
   zero there would read as a real disagreement with Dash0.

   For `cursor`, there is no such channel at all: the interactive TUI has no
   machine-readable output, and print mode — which has one — fires no
   `afterAgentResponse` and so produces no turn to report figures about. Every
   cell in that column reads `-`.

**Three runtimes have one channel `claude` cannot have.**
`plugin-debug.log` is every span the plugin emitted, logged before the wire. It
is the product's own output, not an independent record, so it never supplies an
expectation. What it does is split one failure in two: a span in the log but not
in Dash0 was built and lost in transport or ingest, and a span in neither was
never built. On `claude` those two are indistinguishable from outside.

**The `copilot` runtime's second channel is weaker than it looks, and this is the
one thing to hold on to when reading its report.** `otel.jsonl` is Copilot's own
record, but it is also the plugin's input: tokens, model and every tool span are
read out of it. Its cost figure is not — that stays in the file. So agreement between the `dash0` and `otel` columns
proves the plugin copied its input faithfully, and says nothing about whether
Copilot measured the session correctly. The hook recording remains fully
independent on this runtime, and it can only speak for the session lifecycle —
one `chat` span per `agentStop`. Do not quote a Copilot token comparison as
though it had the standing of the Claude transcript.

`qa-otel.py` prints one further figure that no other channel has: Copilot's own
per-turn roll-up on the top-level `invoke_agent` span, next to the sum of that
turn's `chat` spans. On a plain turn the two agree. **On a delegating turn they
do not, and that is expected** — the roll-up excludes the sub-agent's own chat
spans, while the plugin's flat attribution includes them. `qa-compare.py` says so
where it prints the gap; it is Copilot's arithmetic, not the plugin's.

**Channel one also checks parenting.** `qa-compare.py` verifies that every span's
`parentSpanId` belongs to a span of the same session. Nothing else in the harness
could see a broken trace: a span parented onto an id nobody emitted still counts,
still carries every documented attribute, and still reconciles. Two Codex defects
shipped that way and were found by eye. The check is skipped on a truncated
result, where a missing span would make its children look orphaned. It proves
only that a parent *exists*, not that it is the right one.

Both numeric channels compare *numbers*. Neither can see an attribute nobody
expected: a surplus key changes no span count, so `qa-compare.py` exits `0`
whether or not it is there. `qa/tools/qa-attrs.py` reads the same Dash0 spans
against the attribute tables in `DEVELOPMENT.md`, which is a hand-maintained
contract the pipeline never reads. It is a second question asked of channel one,
not a third channel. Note that it reads what Dash0 *stored*: ingest adds
attributes the plugin never sent, and the tool separates those by grepping the
plugin source, which is deductive and used only to excuse a key, never to
accuse one.

Both tools separate a verdict from a non-reading, and the exit code is how they
say which:

| Exit | `qa-compare.py` | `qa-attrs.py` |
| --- | --- | --- |
| `0` | Every count reconciles | Every observed key is in the contract |
| `1` | A count disagrees | A key is outside the contract |
| `2` | The check could not run: no config, no record, a failed query, or a truncated result | Same, plus a moved `DEVELOPMENT.md` heading |

Never read `2` as either verdict. It means the run was not measured, so the
answer is re-run, not pass and not fail.

The recording is not a third observation channel. It is the input, and treating
it as an observation is the one mistake that would make a run circular.

Known divergences to check before reporting anything:

- **A model in `claude-result.json` but not in Dash0** is the auxiliary-model gap
  in `claude/README.md`: Claude Code's own title-generation call has no hook and
  no assistant transcript entry, so no span exists for it.
- **`gen_ai.request.model` is shorter than the model in the span name.** Release
  0.1.24 emits `chat claude-haiku-4-5-20251001` as the span name and
  `claude-haiku-4-5` as the attribute, on the same span. Compare model *sets*
  loosely until this is resolved; `qa-compare.py` prints both and does not treat
  the difference as a delta.
- **`codex`: the rollout's turn boundary changed name, and `qa-rollout.py` still
  counts the old one.** `internal/source/codex/rollout.go` scoped usage to a turn
  by resetting at an `event_msg` of type `user_message`. codex-cli 0.149.1 writes
  none — `task_started`, `item_completed` and `task_complete` instead — so a
  resumed session never reset and every turn after the first reported the whole
  session. Measured 2026-08-25 on a two-turn resume: turn 2's `chat` span carried
  58594 input tokens for a turn of 29445, having counted turn 1's 29149 twice.
  Fixed the same day by resetting on either name; the re-run reports 29173 and
  29477, each turn its own.

  `qa-rollout.py` deliberately still counts only `user_message`, because it is
  the independent reader and must not be taught the product's rule. So it prints
  `turn boundaries: 0` on a 0.149.1 rollout, and its `turn` figure equals its
  `file` figure. On a single-turn probe that is the same number and the
  comparison is sound. **For a multi-turn `codex` run, `qa-rollout.py`'s `turn`
  column is not a per-turn expectation** — compute the boundaries from
  `task_started` by hand, as the spec for this must.

- **`copilot`: a non-zero shell exit is not a failed tool span.** Measured
  2026-08-28 on `qa/runs/probe-tool-failure`: `exit 3` produced a `postToolUse`
  payload with `"resultType": "success"` and a native `execute_tool` span with no
  error status, so the Dash0 span carries none either. The plugin is faithful
  here; Copilot simply treats a command that ran as a tool that worked. A spec
  about `exception.message` on this runtime needs a tool that fails at the tool
  level, not a command that fails at the shell level.

- **`cursor`: the transcript's `turn_ended` marker is not a turn count.** Measured
  2026-09-01 on `qa/runs/setup-probe-cursor-turns`: a two-turn session wrote six
  message entries and exactly **one** `turn_ended`. It ends the agent loop, not a
  turn. Counting it reported that healthy run as `chat: Dash0 has 2, the
  transcript implies 1`. `qa-transcript-cursor.py` counts the `<user_query>`
  entries instead and reports the markers separately as `loop_ends`.

- **`cursor`: the transcript's tool count is a superset in another vocabulary.**
  Measured 2026-09-01 on `qa/runs/probe-cursor-mcp`: 15 `tool_use` blocks against
  11 `postToolUse` hooks and 11 spans, with every one of the four accounted for.
  Cursor names `Glob` and `Grep` separately in the transcript and reports both as
  `Grep` to a hook, and it records internal plumbing that fires no hook at all —
  `GetDynamicTools` and `CallDynamicTool` carried the MCP call, which reached the
  hooks once as `MCP:echo_text`. `qa-compare.py` prints that table and never
  compares it. **Do not "fix" the gap by filtering the internal names**; that
  teaches the harness the product's rule.

- **`cursor`: a delegating run records a second session and produces no span for
  it.** Measured 2026-09-01 on `qa/runs/probe-cursor-subagent`. This one is a real
  product gap rather than a reading error, and `qa-compare.py` reports it as a
  finding: the sub-agent's tool calls reach a pipeline with no trace context and
  are dropped silently, `subagentStop` never fires, and the `Task` call gets no
  `postToolUse`. The parent's turn is complete and reconciles perfectly, so
  nothing count-based catches it. See
  [findings/cursor-subagent-work-produces-no-span.md](findings/cursor-subagent-work-produces-no-span.md).

- **`cursor`: a shell command that exits non-zero IS a failed tool**, unlike on
  Copilot. Measured 2026-09-01 on `qa/runs/probe-cursor-tool-failure`: `exit 3`
  fired `postToolUseFailure` with `error_message` set, and the span carried status
  code 2 and `exception.message`. So the failure path is reachable here with a
  one-line prompt, and this is the runtime that covers it. Do not carry the
  Copilot learning across.

- **`copilot`: only the tool span carries the skill name.** Measured 2026-08-28
  on `qa/runs/probe-skill`: the `execute_tool skill` span carries
  `dash0.gen_ai.tool.skill.name` and `.source = model`, and the turn's chat span
  carries neither. That matches `DEVELOPMENT.md`, which scopes the chat-span
  route to Claude and Codex — there is no Copilot equivalent of a slash command
  that starts a turn — but a spec carried over from either of those runtimes will
  look for it in the wrong place.

## Settling

Ingest lag only. **Allow 25 seconds, not 8.** Measured 2026-08-28 on
`qa/runs/probe-two-turns-fixed`: 8 seconds after the session ended, Dash0 held
turn 1's two spans and neither of turn 2's, which reads exactly like a plugin
that stopped emitting halfway through; the same query 20 seconds later returned
all four. `qa-compare.py` widens the query window by 60 seconds before the run's
start and 120 seconds after its end, so re-running the comparison is always
enough — the window is not the problem, the wait is. A comparison that reports
too few spans immediately after a session should be re-run before it is believed.

**One thing a Copilot run changes outside its own directory.** On `sessionStart`
the plugin sweeps two places under its data directory: native-OTel files left by
unclean exits, and the session directories of runs that were killed — a session
that ends deletes its own, one that is killed delivers no `sessionEnd`, so
nothing else would. A QA run therefore finds fewer leftovers than it created if
it kills a session and starts another; that is the sweep, not a lost record. The
sub-agent markers under `started/` are never swept, deliberately: losing one ends
a session's telemetry in silence, and they are empty files.

There is no settling inside a session. Every hook exports synchronously before
its process exits. That holds for all three runtimes: `codex exec` and
`copilot -p` are both synchronous and the plugin's hooks POST before their
process exits, so the debug log is complete the moment the command returns, and
only Dash0 lags.

## Checks

Last full pass 2026-08-25 for `claude` and `codex`, against plugin 0.1.25,
`claude` 2.1.238 and codex-cli 0.149.1. The `copilot` checks were added and first
run 2026-08-28 against plugin 0.1.25 and Copilot CLI 1.0.80. The `cursor` checks
were added and first run 2026-09-01 against the working tree at 0.1.26 and
cursor-agent 2026.08.31-4057e58.

All three new-runtime passes behaved the same way: every check ran green except
`qa-attrs.py` on the new runtime's probe, which found real defects each time. On
`copilot` there were three, all fixed the same day and all re-run clean:
`stopReason` copied raw onto every chat span; `github.copilot.cost` exported at
all, now removed; and `dash0.gen_ai.tool.task.name`, a custom key invented to
carry a sub-agent's identity, which is now the standard `gen_ai.agent.*` pair on
a re-emitted `invoke_agent` span. A fourth came from the two-turn probe
rather than from a check; it is described in
`copilot-resumed-turn-is-scoped-to-itself`.

On `cursor` there were **five**, the worst of any runtime, and all of the same
kind: `eventAttributes` copies every payload field nobody denied, and Cursor puts
four of its own on every payload. `conversation_id`, `generation_id`,
`cursor_version` and `workspace_roots` reached every `chat` and `execute_tool`
span; `workspace_roots` is the one that mattered, since it is a JSON array of
absolute paths on a raw key `omit_io` does not cover. `failure_type` was the
fifth and only the failure probe could reach it. All five are denied now and both
probes re-run with `qa-attrs.py` exiting `0`, at 37 observed keys instead of 41.

The `cursor` pass also turned up two harness defects and one product gap that no
check would have caught. The harness ones are fixed: the transcript reader counted
`turn_ended` markers as turns, and `qa-compare.py` reported a sub-agent's session
as a reused run id. The product gap is open —
[findings/cursor-subagent-work-produces-no-span.md](findings/cursor-subagent-work-produces-no-span.md).

Checks with no prefix apply to every runtime. A `codex-`, `copilot-` or `cursor-`
prefix means the check belongs to that runtime alone; skip it when a run targets
another one, and skip the `claude`-only ones the same way. The runtime-specific
blocking checks are `probe-session-agrees-with-what-it-was-fed` for `claude`,
`codex-probe-session-agrees-with-what-it-was-fed` for `codex`,
`copilot-probe-session-agrees-with-what-it-was-fed` for `copilot`, and
`cursor-probe-session-agrees-with-what-it-was-fed` for `cursor`.

### toolchain-present

- **proves.** The run needs `go`, `python3`, `claude`, `dash0`, and `uuidgen`, and
  a missing one surfaces halfway through a paid session rather than before it.
- **after.** none
- **blocking.** true
- **pass.** No output.
- **fail.** `MISSING: <tool>`. `claude` comes from
  `npm install -g @anthropic-ai/claude-code`, `dash0` from `brew install dash0`.
- **verified.** 2026-08-21, signals: pass+fail

```sh
for t in go python3 claude dash0 uuidgen; do command -v "$t" >/dev/null || echo "MISSING: $t"; done
```

### qa-runs-is-untracked

- **proves.** A run directory holds prompts, responses, full transcripts, and
  every hook payload. Committing one puts session content in git history
  permanently.
- **after.** none
- **blocking.** true
- **pass.** `ignored`, and every tool path prints `tracked`.
- **fail.** `NOT ignored` for `qa/runs` means the `qa/runs/` line is gone from
  `.gitignore`; restore it before running anything. `IGNORED` for a tool path is
  the opposite problem: `.gitignore` has a bare `bin/` rule that matches any
  directory named `bin` at any depth, which is why these live in `qa/tools/` and
  must not move to `qa/bin/`.
- **verified.** 2026-08-25, signals: pass+fail. The three Codex tool paths were
  added that day and the pass half re-ran. The fail half was provoked on
  2026-08-21 against the original three, and it is the same `.gitignore` rule.

```sh
# The trailing slash matters. The .gitignore rule is `qa/runs/`, which only
# matches a directory, and `git check-ignore qa/runs` returns 1 when the
# directory does not exist yet — a fresh clone or worktree fails this check for
# no reason and the fix looks like an editing job on .gitignore.
git check-ignore -q qa/runs/ && echo ignored || echo "NOT ignored"
for p in qa/tools/qa-session.sh qa/tools/qa-compare.py qa/tools/qa-attrs.py \
         qa/tools/qa-session-codex.sh qa/tools/qa-rollout.py \
         qa/tools/qa-codex-hooks/main.go \
         qa/tools/qa-session-copilot.sh qa/tools/qa-otel.py \
         qa/tools/qa-session-cursor.sh qa/tools/qa-cursor-drive.py \
         qa/tools/qa-transcript-cursor.py; do
  git check-ignore -q "$p" && echo "IGNORED: $p" || echo "tracked: $p"
done
```

### config-is-complete

- **proves.** The config exists, parses, and has no placeholder left. Every other
  check that touches Dash0 fails confusingly without it, and a leftover
  `auth_REPLACE_ME` fails as a 401 that reads like an expired credential.
- **after.** none
- **blocking.** true
- **pass.** `config ok`.
- **fail.** The message names what is wrong. `does not exist` — copy
  `qa/config.local.json.example` and fill it in with the values the team keeps
  for QA. `missing: <keys>` — add them. `placeholder` — the token was never
  filled in. Only a person can supply these, so stop and ask.
- **verified.** 2026-08-21, signals: pass+fail

```sh
python3 -c "
import json, sys
try:
    c = json.load(open('qa/config.local.json'))
except FileNotFoundError:
    sys.exit('qa/config.local.json does not exist')
except json.JSONDecodeError as e:
    sys.exit(f'qa/config.local.json is not valid JSON: {e}')
missing = [k for k in ('apiUrl','appUrl','ingestUrl','authToken','dataset') if not c.get(k)]
if missing: sys.exit('missing: ' + ', '.join(missing))
if any('REPLACE_ME' in str(v) for v in c.values()): sys.exit('placeholder left in the config')
print('config ok')
"
```

### config-is-untracked

- **proves.** The live token is not in git. This runs before anything else reads
  the file, because if it fails the credential is already in history and the fix
  is not a code change.
- **after.** none
- **blocking.** true
- **pass.** `untracked` and `gitignored`.
- **fail.** `TRACKED` means the token is in the repository. Stop immediately, tell
  the user, and do not run anything else — the token has to be rotated, which
  only a person can do. `NOT gitignored` means the `qa/config.local.json` line is
  missing from `.gitignore`; add it before the next commit.
- **verified.** 2026-08-21, signals: pass+fail

```sh
git ls-files --error-unmatch qa/config.local.json >/dev/null 2>&1 && echo TRACKED || echo untracked
git check-ignore -q qa/config.local.json && echo gitignored || echo "NOT gitignored"
```

### qa-reads-the-environment-the-plugin-writes-to

- **proves.** The read side and the write side are the same place. The plugin's
  target comes from the managed install and QA cannot change it, so the config
  has to agree with it rather than the other way round. Disagreement produces a
  perfectly healthy run with zero spans found, which reads as total telemetry
  loss.
- **after.** config-is-complete
- **blocking.** true
- **pass.** `MATCH`.
- **fail.** `MISMATCH` names which half differs. Change `qa/config.local.json` to
  match the install — never the install to match the config, because the install
  is what is under test. `(not configured)` for the plugin side means the local
  install has no OTLP target at all, so no session will export anything; that is
  a `/plugin` configuration problem, not a QA one.
- **verified.** 2026-08-21, signals: pass+fail

```sh
python3 -c "
import json, os
cfg = json.load(open('qa/config.local.json'))
remote = os.path.expanduser('~/.claude/remote-settings.json')
opts = {}
if os.path.exists(remote):
    opts = ((json.load(open(remote)).get('pluginConfigs') or {})
            .get('dash0-agent-plugin@dash0') or {}).get('options') or {}
url, dataset = opts.get('OTLP_URL',''), opts.get('DATASET','') or 'default'
print(f'plugin writes to: {url or \"(not configured)\"} / {dataset}')
print(f'QA reads from   : {cfg[\"ingestUrl\"]} / {cfg[\"dataset\"]}')
same = url == cfg['ingestUrl'], dataset == cfg['dataset']
print('MATCH' if all(same) else
      f'MISMATCH (endpoint {\"ok\" if same[0] else \"differs\"}, dataset {\"ok\" if same[1] else \"differs\"})')
"
```

### token-reads-the-dataset

- **proves.** The configured token actually reads the configured dataset. A wrong
  token and a wrong dataset both fail here, and they fail differently, which is
  what makes this worth running before a paid session.
- **after.** config-is-complete, qa-reads-the-environment-the-plugin-writes-to
- **blocking.** true
- **pass.** A CSV header and at least one row.
- **fail.** `401 The provided auth token is not known` — the token is wrong or
  rotated; ask the team for a fresh one. `403 access to dataset '<name>' is not
  permitted` — either the token has no access or the dataset does not exist, and
  the API cannot tell those apart, so check the spelling against the plugin's
  `DATASET` first. A header with no rows means nobody has run a session in the
  window; widen `--from` before concluding anything.

  This check cannot prove the token is unrestricted, only that it reads this one
  dataset. That is all this project ever needs, since it reads exactly one.
- **verified.** 2026-08-21, signals: pass+fail

```sh
python3 - <<'PY' | sh
import json
c = json.load(open('qa/config.local.json'))
print(f"dash0 spans query --api-url {c['apiUrl']} --auth-token {c['authToken']} "
      f"--dataset {c['dataset']} --precision disabled --from now-3h --limit 5 "
      f"--filter 'service.name is claude' --column timestamp --column 'span name' -o csv")
PY
```

### no-project-config-overrides-the-install

- **proves.** No config file in the session's reach can hand the installed plugin
  a QA auth token. This is the failure that cost the most time to find: the
  export goes to the right endpoint with the wrong credential, Dash0 returns 401,
  and the run reports zero spans as though the plugin were broken.
- **after.** none
- **blocking.** true
- **pass.** `clean` and `driver clean`.
- **fail.** A path from the first command means this repository has a
  project-level config file, and a session run from here would hand its token to
  the installed plugin. Move it to `~/.claude/` — a *user-level* file is the
  right place for a developer's own configuration and is invisible to a QA
  project. `driver WRITES A CONFIG FILE` means the regression is back in
  `qa-session.sh`; remove that write.

  The check deliberately looks at the driver rather than at existing run
  directories. A config file in an old run's project cannot affect a new session,
  because a session only reads `.claude/` relative to its own working directory,
  so scanning `qa/runs/` produces failures that mean nothing.
- **verified.** 2026-08-21, signals: pass+fail

```sh
ls .claude/dash0-agent-plugin.local.md 2>/dev/null || echo clean
# Comment lines are skipped: the driver names the file in a comment explaining
# why it must not write it, and matching that would fail the check forever.
grep -qE '^[[:space:]]*[^#[:space:]].*dash0-agent-plugin\.local\.md' qa/tools/qa-session.sh \
  && echo "driver WRITES A CONFIG FILE" || echo "driver clean"
```

### probe-session-agrees-with-what-it-was-fed

- **proves.** The whole method on a session small enough to reason about: the
  recorder saw every hook, the installed plugin exported, Dash0 stored it, and the
  span counts, tool names, and token counts agree with the hooks and the
  transcript. Each of those has failed silently in a way that looks like a product
  bug from one channel alone.
- **after.** token-reads-the-dataset, no-project-config-overrides-the-install
- **blocking.** true
- **pass.** `All three records agree.` and exit `0`.
- **fail.** Exit `1` prints each difference. Read the `hooks` column first: a span
  Dash0 lacks that the hooks imply is the plugin's or the transport's fault, and a
  span Dash0 has that the hooks do not imply came from somewhere else — check the
  conversation id. Exit `2` means a channel was unavailable, not that a count was
  zero; the message says which. `0 spans` with a healthy recording is usually
  ingest lag, so re-run the comparison before filing anything.
- **verified.** 2026-08-21, signals: pass+fail

```sh
QA_MODEL=haiku qa/tools/qa-session.sh 'Run the bash command: echo qa-probe. Then read the file settings.json in .claude. Then reply with exactly the word done.' setup-probe
sleep 8
qa/tools/qa-compare.py qa/runs/setup-probe
```

### sub-agent-transcript-is-captured

- **proves.** A sub-agent's usage lives only in its own transcript, and its event
  carries `agent_transcript_path`. A recorder that misses it produces fixtures
  that cannot explain an `invoke_agent` span, and the audit script names this as
  the usual reason usage appears in a transcript but not in telemetry.
- **after.** probe-session-agrees-with-what-it-was-fed
- **blocking.** false. Without it, findings about sub-agent spans have no
  independent input record, so they ship as single-channel.
- **pass.** At least one `SubagentStop`, at least one captured
  `agent_transcript_sha256`, and `invoke_agent` agreeing across all three columns.
- **fail.** `SubagentStop` recorded with no `agent_transcript_sha256` means the
  payload field changed name; re-read `internal/source/` and repair the recorder.
  No `SubagentStop` at all means the model did not delegate — reword the prompt
  rather than concluding anything.
- **verified.** 2026-08-21, signals: pass-only. Provoking the failure needs a
  payload without `agent_transcript_path`, which cannot be arranged from outside
  the host.

```sh
QA_MODEL=haiku QA_ALLOWED_TOOLS="Task Agent Bash" qa/tools/qa-session.sh \
  'Use the Task tool (subagent_type general-purpose) to ask a sub-agent to run the bash command: echo qa-sub. When it returns, reply with exactly the word done.' setup-probe-sub
sleep 8
python3 -c "
import json
rows = [json.loads(l) for l in open('qa/runs/setup-probe-sub/record/index.jsonl')]
print('SubagentStop:', sum(1 for r in rows if r['hook_event_name'] == 'SubagentStop'))
print('sub-agent transcripts:', sum(1 for r in rows if r.get('agent_transcript_sha256')))
"
qa/tools/qa-compare.py qa/runs/setup-probe-sub
```

### installed-binary-is-restored

- **proves.** `QA_SWAP_BINARY=1` left the machine as it found it. The swap
  overwrites the binary cache that the developer's own live sessions use, so a run
  that dies without restoring silently changes every later session's telemetry.
- **after.** none
- **blocking.** true
- **pass.** `RESTORED`.
- **fail.** `NOT RESTORED` — copy `qa/runs/<id>/installed-binary.bak` back over
  the cache path yourself. If no backup exists, delete the cached binary and the
  bootstrap re-downloads the release on the next session.
- **verified.** 2026-08-21, signals: pass-only. Provoking the failure means
  killing a run between the swap and the trap, which would leave the machine in
  the state this check exists to prevent.

```sh
BIN="$HOME/.claude/plugins/data/dash0-agent-plugin-dash0/bin/on-event-0.1.24-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
before=$(shasum -a 256 "$BIN" | cut -d' ' -f1)
QA_SWAP_BINARY=1 QA_MODEL=haiku qa/tools/qa-session.sh 'Reply with exactly the word done.' setup-probe-swap
[ "$before" = "$(shasum -a 256 "$BIN" | cut -d' ' -f1)" ] && echo RESTORED || echo "NOT RESTORED"
```

### run-dir-carries-no-real-credential

- **proves.** A run directory can be attached to a bug report. It holds every hook
  payload and a full transcript, and there are two live tokens on this machine
  that could end up in one: `.env`'s ingest token, and `qa/config.local.json`'s
  `authToken`, which `qa-compare.py` passes on a command line and the `codex`,
  `copilot` and `cursor` drivers hand to a real install.
- **after.** config-is-complete
- **blocking.** true
- **pass.** `control ok` for each token, then `clean` for every scan. The control
  halves matter: a grep that matches nothing anywhere proves nothing about the run
  directories.
- **fail.** Any path printed is a leaked credential. Delete that run directory and
  find out how the value got there before running again. `control missing` means
  the check tested nothing, so fix the check rather than trusting it.
- **verified.** 2026-08-25, signals: pass+fail. Re-run after the first `codex`
  probe with all three secrets configured: `control ok` for both config tokens,
  19 files scanned, `clean`. The fail half was provoked on 2026-08-21 and the
  scan is the same one.

```sh
python3 - <<'PY'
import glob, json, os, sys

secrets = {}
if os.path.exists('.env'):
    for line in open('.env'):
        if line.startswith('DASH0_AUTH_TOKEN='):
            secrets['.env DASH0_AUTH_TOKEN'] = line.split('=', 1)[1].strip()
if os.path.exists('qa/config.local.json'):
    secrets['config authToken'] = json.load(open('qa/config.local.json'))['authToken']

if not secrets:
    sys.exit('control missing: no token found to scan for')

files = [p for p in glob.glob('qa/runs/**/*', recursive=True) if os.path.isfile(p)]
leaked = False
for name, value in secrets.items():
    if not value:
        print(f'control missing: {name} is empty')
        continue
    print(f'control ok: {name}')
    for path in files:
        try:
            with open(path, 'rb') as handle:
                if value.encode() in handle.read():
                    print(f'  LEAK: {value[:6]}... from {name} appears in {path}')
                    leaked = True
        except OSError:
            pass
print('clean' if not leaked else 'LEAKED')
PY
```

### codex-toolchain-present

- **proves.** A `codex` run needs `codex`, `go`, `python3`, and `git` on top of
  what the shared checks cover. A missing one surfaces after the throwaway home
  is built and, with auth in place, after the session has already been paid for.
- **after.** none
- **blocking.** true
- **pass.** No output.
- **fail.** `MISSING: <tool>`. `codex` comes from `brew install codex` or
  `npm install -g @openai/codex`. `git` is needed because the driver creates a
  real repository for the session to work in, which is what `internal/vcs` reads.
- **verified.** 2026-08-25, signals: pass+fail

```sh
for t in codex go python3 git; do command -v "$t" >/dev/null || echo "MISSING: $t"; done
```

### ingest-token-reaches-the-ingress

- **proves.** The token actually reaches the ingress, which the `codex` and
  `copilot` runtimes need and the `claude` runtime does not. It carries no runtime
  prefix for that reason: both provisioning runtimes hand the config's one
  `authToken` to a real install, and a token the ingress rejects 401s while the session itself
  runs perfectly: the report then says zero spans, which reads as total telemetry
  loss rather than as a QA misconfiguration. `token-reads-the-dataset` proves the
  other direction and cannot see this one — the two permissions are separate, and
  a token can genuinely have one without the other. Measured 2026-08-26: an
  ingest-scoped token answers a query with `403 ... *:read permission is
  required`, so the asymmetry is real in both directions.
- **after.** config-is-complete
- **blocking.** true
- **pass.** `401` for the control, then `400` for the configured token: the
  ingress authenticated it and rejected the deliberately malformed body instead.
- **fail.** `401` for the configured token means the ingress does not accept it.
  Ask the team for one that both reads and ingests; only a person can supply it,
  so stop and ask. Anything other than `401` for the control means the probe
  proved nothing — a wrong endpoint, or something in front of it answering — so
  fix the check rather than trusting it.
- **verified.** 2026-08-26, signals: pass+fail, and re-run 2026-08-28 with both
  signals again. The malformed body is what keeps this free of side effects: it
  exercises authentication without ingesting a span into a shared dataset.
  Confirmed end to end as well, by running a Codex session whose provisioned
  install was given this token and reading its 2 spans back.

  Confirmed for `copilot` the same way on 2026-08-28, on `qa/runs/probe-single-token`:
  a provisioned Copilot install was handed the config's `authToken`, its session
  exported, and its 2 spans were read back.

```sh
python3 -c "
import json
c = json.load(open('qa/config.local.json'))
probe = (\"curl -s -o /dev/null -w '%{http_code}\n' -X POST \" + c['ingestUrl'] +
         \"/v1/traces -H 'Content-Type: application/json' --data '{ not json'\"
         \" -H 'Authorization: Bearer \")
print(\"printf '  control (bogus token): '\")
print(probe + \"auth_definitely_not_a_real_token_00000'\")
print(\"printf '  configured authToken : '\")
print(probe + c['authToken'] + \"'\")
" | sh
```

### codex-recorder-is-trusted-and-the-plugin-keeps-its-index

- **proves.** Codex runs a hook only when `config.toml` carries a matching
  `trusted_hash`, and **skips an untrusted one in silence** — no prompt, no log
  line, no non-zero exit. Two ways to get there, both of which this catches: the
  recorder's reproduced hash is wrong, or the recorder's block took a group index
  the plugin's block also claims. Either way the session runs, the report says
  zero recorded hooks, and nothing on the machine says why.

  It costs nothing to run. The throwaway home has no credential, so Codex fails
  at auth after `SessionStart` and `UserPromptSubmit` have already fired — which
  is all this needs. No model call, no tokens, no ingest.
- **after.** codex-toolchain-present
- **blocking.** true
- **pass.** `:session_start:0:0 :session_start:1:0` on the first line, then
  `SessionStart` among the recorded events and `RECORDED` on the last.
- **fail.** `NOTHING RECORDED` means Codex rejected the recorder's trust entry.
  Check the first line before anything else: two indices that are not `0` and `1`
  mean the blocks were written in the wrong order, and `qa-session-codex.sh` must
  register the recorder before it runs `install-codex.sh`. Two correct indices
  with nothing recorded means the hash itself is stale — compare
  `internal/source/codex/trust.go` against the installed `codex --version`, and
  expect `test/e2e`'s no-bypass canary to be failing too.
- **verified.** 2026-08-25, signals: pass+fail. The fail half was provoked by
  corrupting every `trusted_hash` in the generated config, against codex-cli
  0.149.1: nothing was recorded, and Codex reported no error of any kind.

```sh
S=$(mktemp -d); mkdir -p "$S/.codex" "$S/state" "$S/record" "$S/project"
go build -o "$S/recorder" ./qa/recorder && go build -o "$S/qa-codex-hooks" ./qa/tools/qa-codex-hooks
printf '#!/usr/bin/env bash\nexport QA_RECORD_DIR="%s/record"\nexec "%s/recorder"\n' "$S" "$S" >"$S/hook.sh"
chmod +x "$S/hook.sh"
"$S/qa-codex-hooks" --command "$S/hook.sh" --config "$S/.codex/config.toml" >"$S/block.toml"
mv "$S/block.toml" "$S/.codex/config.toml"
V=$(grep '^VERSION=' codex/codex-on-event.sh | cut -d'"' -f2)
O=$(uname -s | tr '[:upper:]' '[:lower:]'); A=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
mkdir -p "$S/state/dash0-agent-plugin/codex/bin"
go build -o "$S/state/dash0-agent-plugin/codex/bin/codex-on-event-$V-$O-$A" ./cmd/codex-on-event
install -m 755 codex/codex-on-event.sh "$S/state/dash0-agent-plugin/codex/codex-on-event.sh"
# No endpoint and no token: the plugin installs and stays inactive, which is what
# keeps this check free. DASH0_TEAM_NAME must be set or the installer blocks on a
# /dev/tty prompt for it.
env HOME="$S" XDG_STATE_HOME="$S/state" DASH0_VERSION="$V" DASH0_TEAM_NAME=dash0-qa \
  DASH0_OTLP_URL= DASH0_AUTH_TOKEN= DASH0_DATASET=default \
  bash install-codex.sh >/dev/null 2>&1
grep -o ':session_start:[01]:0' "$S/.codex/config.toml" | sort | tr '\n' ' '; echo
git -C "$S/project" init -q
timeout 90 env HOME="$S" CODEX_HOME="$S/.codex" XDG_STATE_HOME="$S/state" \
  codex exec --cd "$S/project" --sandbox read-only -c 'approval_policy="never"' \
  'reply done' </dev/null >/dev/null 2>&1
grep -o '"hook_event_name":"[A-Za-z]*"' "$S/record/index.jsonl" 2>/dev/null | sort -u | tr '\n' ' '
[ -s "$S/record/index.jsonl" ] && echo "RECORDED" || echo "NOTHING RECORDED"
rm -rf "$S"
```

### codex-driver-writes-no-project-config

- **proves.** The bootstrap prefers `.codex/dash0-agent-plugin.local.md` in the
  working directory over the global one, so a project-level config in the session's
  workspace would retarget the install the run just provisioned. The run would
  export somewhere else, or nowhere, and read as a plugin that sends nothing.

  It reads the driver rather than an existing run directory on purpose. A config
  file in an old run's project cannot affect a new session, so scanning
  `qa/runs/` produces failures that mean nothing. The driver writing one is the
  only way this can happen.
- **after.** none
- **blocking.** true
- **pass.** `driver clean`.
- **fail.** `driver WRITES A PROJECT CONFIG` means the driver names that file
  under `$PROJECT`; remove the write. The driver does legitimately edit the
  *global* config in the throwaway home, to turn on the debug log, and that line
  names `$SCRATCH` rather than `$PROJECT` — which is exactly the distinction this
  check makes, so do not "fix" it by widening the pattern.
- **verified.** 2026-08-25, signals: pass+fail. The fail half was provoked on a
  copy of the driver with a `$PROJECT`-scoped write appended.

```sh
grep -nE '^[[:space:]]*[^#[:space:]].*dash0-agent-plugin\.local\.md' qa/tools/qa-session-codex.sh |
  grep -q 'PROJECT' && echo "driver WRITES A PROJECT CONFIG" || echo "driver clean"
```

### codex-probe-session-agrees-with-what-it-was-fed

- **proves.** The whole method on a Codex session small enough to reason about:
  the recorder saw every hook, the provisioned install exported, Dash0 stored it,
  and the span counts, tool names, and token counts agree with the hooks and the
  rollout. This runtime needs no equivalent of
  `qa-reads-the-environment-the-plugin-writes-to`, because the driver writes and
  reads the same `ingestUrl` and `dataset` by construction — which means a
  mismatch cannot be the explanation when this fails, and something real is.
- **after.** ingest-token-reaches-the-ingress,
  codex-recorder-is-trusted-and-the-plugin-keeps-its-index, token-reads-the-dataset
- **blocking.** true
- **pass.** `All three records agree.` and exit `0`.
- **fail.** Exit `1` prints each difference; read the `hooks` column first, as in
  the `claude` probe. Exit `2` means a channel was unavailable. Two failures are
  specific to this runtime: `no rollout.jsonl in the run` means the driver found
  no rollout, so the usage channel is missing rather than zero; and a span count
  of zero **with** a non-zero `spans_logged` in the manifest means the plugin
  built the spans and they were lost after it — a transport or ingest problem,
  not a pipeline one. That split is the whole reason the debug log is on.
- **verified.** 2026-08-25, signals: pass-only. The whole path ran green against
  release 0.1.25 and codex-cli 0.149.1: 5 hooks recorded, 1 `chat` and 1
  `execute_tool` in Dash0, and input, output and cache-read token counts equal
  across all three channels to the token. Provoking a failure would mean breaking
  the export on purpose, which this check exists to notice rather than to cause.

  `qa-attrs.py` exited `1` on that first run, on a real finding rather than a
  setup problem: Codex spans carried `turn_id`, a raw undeclared attribute, because
  the attribute copy is a deny list and nothing denied it. Fixed the same day in
  `attrSkipKeys`, and the re-run against the working-tree binary exits `0` with 45
  observed keys instead of 46. Both tools exiting `0` is the pass signal now.
- **shape.** Measured on the probe above: 5 hook invocations, 4 distinct rollout
  snapshots, 2 spans, 2 `token_count` events, and **0 turn boundaries**. Read the
  divergence note in `## Observe` before drawing anything from that last number.

```sh
qa/tools/qa-session-codex.sh \
  'Run the shell command: echo qa-probe. Then reply with exactly the word done.' \
  setup-probe-codex
sleep 8
qa/tools/qa-compare.py qa/runs/setup-probe-codex
qa/tools/qa-attrs.py qa/runs/setup-probe-codex
```

### copilot-toolchain-present

- **proves.** A `copilot` run needs `copilot`, `go`, `python3`, `git` and
  `uuidgen` on top of what the shared checks cover. A missing one surfaces after
  the throwaway home is built and, with auth in place, after the session has
  already been paid for.
- **after.** none
- **blocking.** true
- **pass.** No output, then a version line.
- **fail.** `MISSING: <tool>`. `copilot` comes from
  `npm install -g @github/copilot`. `git` is needed because the driver creates a
  real repository for the session to work in, which is what `internal/vcs` reads;
  `uuidgen` supplies the pinned session id.
- **verified.** 2026-08-28, signals: pass+fail

```sh
for t in copilot go python3 git uuidgen; do command -v "$t" >/dev/null || echo "MISSING: $t"; done
copilot --version | head -1
```

### copilot-auth-is-available

- **proves.** A GitHub token that Copilot accepts is reachable. The throwaway
  home carries no stored login — that is the point of it — so a run without one
  of these fails at auth, after the install and before the model. The failure is
  cheap but it is also silent in the run's own output: the session exits and the
  recording holds a `sessionStart` and nothing else.
- **after.** copilot-toolchain-present
- **blocking.** true
- **pass.** `token available`.
- **fail.** `NO TOKEN` — export `COPILOT_GITHUB_TOKEN` (or `GH_TOKEN`, or
  `GITHUB_TOKEN`), or run `gh auth login`. Only a person can supply a credential,
  so stop and ask.

  This proves a token *exists*, not that GitHub will accept it for Copilot: the
  entitlement is on the account, not the token, and the API cannot be asked for
  free. `copilot-probe-session-agrees-with-what-it-was-fed` is where a rejected
  token shows up.
- **verified.** 2026-08-28, signals: pass+fail. The pass half used a `gh auth
  token` scoped `gist, read:org, repo`, which drove a real session end to end.

```sh
# The braces matter. Without them `a || b && c || d` groups as `(a || b) && c`
# only by accident of left-to-right evaluation, and a shell reading it the other
# way reports NO TOKEN on a machine that has one.
{ [ -n "${COPILOT_GITHUB_TOKEN:-${GH_TOKEN:-${GITHUB_TOKEN:-}}}" ] ||
  [ -n "$(gh auth token 2>/dev/null)" ]; } &&
  echo "token available" || echo "NO TOKEN"
```

### copilot-driver-writes-no-project-config

- **proves.** The bootstrap prefers `.copilot/dash0-agent-plugin.local.md` in the
  working directory over the global one, so a project-level config in the
  session's workspace would retarget the install the run just provisioned. The
  run would export somewhere else, or nowhere, and read as a plugin that sends
  nothing.

  It reads the driver rather than an existing run directory on purpose. A config
  file in an old run's project cannot affect a new session, so scanning
  `qa/runs/` produces failures that mean nothing. The driver writing one is the
  only way this can happen.
- **after.** none
- **blocking.** true
- **pass.** `driver clean`.
- **fail.** `driver WRITES A PROJECT CONFIG` means the driver names that file
  under `$PROJECT`; remove the write. The driver does legitimately write the
  *global* config in the throwaway home, and that line names `$COPILOT_HOME_DIR`
  rather than `$PROJECT` — which is exactly the distinction this check makes, so
  do not "fix" it by widening the pattern.
- **verified.** 2026-08-28, signals: pass+fail. The fail half was provoked on a
  copy of the driver with a `$PROJECT`-scoped write appended.

```sh
grep -nE '^[[:space:]]*[^#[:space:]].*dash0-agent-plugin\.local\.md' qa/tools/qa-session-copilot.sh |
  grep -q 'PROJECT' && echo "driver WRITES A PROJECT CONFIG" || echo "driver clean"
```

### copilot-recorder-covers-every-plugin-event

- **proves.** The recorder is registered for every event the plugin acts on. The
  list is generated from `copilot/hooks.json` at run time, so this asserts the
  generation rather than a copy: if the plugin grows an event and the driver's
  extra list happens to shadow it, the recording would silently stop covering the
  pipeline's input while still looking full.

  It costs nothing — no Copilot process, no token, no model call. It runs the
  driver's own generator against the shipped hooks file.
- **after.** none
- **blocking.** true
- **pass.** `covers every plugin event`, then the event list.
- **fail.** `MISSING: <event>` means an event in `copilot/hooks.json` is absent
  from the generated registration; the generator in `qa-session-copilot.sh` is
  what to repair. `preToolUse REGISTERED` is the opposite failure and a serious
  one: that is Copilot's only fail-closed event, so a recorder there can block the
  session's tools.
- **verified.** 2026-08-28, signals: pass+fail. The fail half was provoked by
  adding `preToolUse` to the extra list and by removing `agentStop` from a copy
  of `copilot/hooks.json`.

```sh
python3 - <<'PY'
import json, re, subprocess
# Anchored on the hooks file the generator consumes, not on the heredoc marker:
# the driver has several `<<'PY'` blocks and the first one is the config reader.
generator = re.search(r"copilot/hooks\.json\".*?<<'PY'\n(.*?)\nPY\n",
                      open('qa/tools/qa-session-copilot.sh').read(), re.S).group(1)
out = subprocess.run(['python3', '-c', generator, 'copilot/hooks.json', '/tmp/qa-recorder-probe'],
                     capture_output=True, text=True, check=True).stdout
registered = json.loads(out)['hooks']
missing = [e for e in json.load(open('copilot/hooks.json'))['hooks'] if e not in registered]
print('MISSING: ' + ', '.join(missing) if missing else 'covers every plugin event')
if 'preToolUse' in registered:
    print('preToolUse REGISTERED — that event is fail-closed and must never carry a QA hook')
print(', '.join(registered))
PY
```

### copilot-probe-session-agrees-with-what-it-was-fed

- **proves.** The whole method on a Copilot session small enough to reason about:
  the recorder saw every hook, the marketplace install exported, Dash0 stored it,
  and the span counts, tool names and token counts agree with the hooks and the
  native-OTel file. This runtime needs no equivalent of
  `qa-reads-the-environment-the-plugin-writes-to`, because the driver writes and
  reads the same `ingestUrl` and `dataset` by construction — which means a
  mismatch cannot be the explanation when this fails, and something real is.
- **after.** copilot-auth-is-available, copilot-driver-writes-no-project-config,
  ingest-token-reaches-the-ingress, token-reads-the-dataset
- **blocking.** true
- **pass.** `All three records agree.` and exit `0` from `qa-compare.py`, then
  exit `0` from `qa-attrs.py`.
- **fail.** Exit `1` prints each difference. Read the `hooks` column first, but
  remember it only claims a `chat` span on this runtime — a tool or token
  difference is against the OTel file, which is also the plugin's input, so it
  means the plugin failed to copy rather than that Copilot measured wrongly. Exit
  `2` means a channel was unavailable. Two failures are specific to this runtime:
  `no otel*.jsonl in the run` means native OTel wrote nothing, so usage and tools
  are unavailable rather than zero; and a span count of zero **with** a non-zero
  `spans_logged` in the manifest means the plugin built the spans and they were
  lost after it, which is a transport or ingest problem rather than a pipeline
  one.

  **Wait 25 seconds, not 8.** See `## Settling`: a shorter wait returned turn 1's
  spans and not turn 2's, which reads exactly like a product bug.
- **verified.** 2026-08-28, signals: pass-only. The whole path ran green against
  Copilot CLI 1.0.80: 5 hooks recorded, 1 `chat` and 1 `execute_tool` in Dash0,
  and input, output, cache-read and reasoning token counts equal across Dash0 and
  the OTel file to the token. Provoking a failure would mean breaking the export
  on purpose, which this check exists to notice rather than to cause.

  `qa-attrs.py` exited `1` on the first run, on three real findings rather than a
  setup problem. `stopReason` reached every chat span as a raw payload field,
  because the attribute copy is a deny list and nothing denied it.
  `dash0.gen_ai.tool.task.name` was a real export the contract did not list, and
  documenting it turned out to be the wrong fix: it is a custom key, so a backend
  feature would never read it. It is gone, and a sub-agent now gets its own
  `invoke_agent` span carrying the standard `gen_ai.agent.name` and
  `gen_ai.agent.id` instead. `github.copilot.cost` was also a real export, and
  it went the other way: it is Copilot's accounting in AI credits and would have
  collided with the money figure Dash0 derives at ingest, so the export was
  removed and the key denied. All three were fixed the same day, and the re-run
  exits `0` with 39 observed keys instead of 41, against 60 documented. Both tools exiting `0` is the
  pass signal now.
- **shape.** Measured on the probe below: 5 hook invocations
  (`sessionStart`, `userPromptSubmitted`, `postToolUse`, `agentStop`,
  `sessionEnd`), 1 native-OTel file holding 1 `invoke_agent`, 2 `chat` and 1
  `execute_tool` span, and 2 spans in Dash0. The two `chat` spans are model
  round-trips, not turns: the plugin sums them into the turn's one `chat` span.

```sh
QA_COPILOT_BINARY=working-tree qa/tools/qa-session-copilot.sh \
  'Run the shell command: echo qa-probe. Then reply with exactly the word done.' \
  setup-probe-copilot
sleep 25
qa/tools/qa-compare.py qa/runs/setup-probe-copilot
qa/tools/qa-attrs.py qa/runs/setup-probe-copilot
```

### copilot-resumed-turn-is-scoped-to-itself

- **proves.** The per-turn boundary, which is the one thing a single-turn probe
  cannot see: with one turn, "this turn's usage" and "the session's usage" are the
  same number and a double-count is invisible. Copilot's hooks carry no usage at
  all, so the whole mechanism is the cursor in
  `internal/source/copilot/otelfile.go` — the id of the last native span already
  consumed.

  This is not a hypothetical. Run first on 2026-08-28, it failed: turn 2's `chat`
  span carried 59068 input tokens for a turn of 29655, having counted turn 1's
  29413 a second time, and turn 1's `execute_tool` span was emitted again under
  turn 2's trace. The cursor lived in the per-session directory that
  `pipeline.Process` deletes on `SessionEnd`, and a Copilot session id outlives
  its session, so a resumed launch found no cursor and re-read the file from the
  start. Fixed the same day by keying the cursor by conversation and keeping it
  beside the OTel files, where the existing stale-file sweep also cleans it up;
  the re-run reports 29417 and 29711, each turn its own.

  Only reachable when both launches share one native-OTel file. The launch
  function the `dash0-configure` skill installs gives each launch its own and
  deletes it at exit, which made the stale cursor harmless; a fixed
  `COPILOT_OTEL_FILE_EXPORTER_PATH`, the documented alternative to that function,
  does not. The driver shares one file deliberately for this reason.
- **after.** copilot-probe-session-agrees-with-what-it-was-fed
- **blocking.** false. Without it, a Copilot run says nothing about per-turn
  scoping, and a spec that needs it ships single-channel.
- **pass.** Exit `0`, `2` `chat` and `2` `execute_tool` spans in Dash0, and each
  turn's input tokens close to the other's rather than double.
- **fail.** Two different shapes, and they are not the same problem.

  Token counts roughly 1.5× the OTel file's and one tool span too many is the
  regression above: check that the cursor file
  `~/.local/state/dash0-agent-plugin/copilot/otel/cursor-<session>.json` exists
  in the throwaway home after turn 1, using `QA_KEEP_SCRATCH=1`.

  Turn 2 missing **entirely** — 2 spans where there should be 4, exactly turn 1's
  numbers, `"spans_logged": 2` against `"turns": 2` — is not a plugin defect at
  all. The plugin never ran: Copilot CLI 1.0.81 serves a local-directory
  marketplace by reference, and a live-loaded plugin's hooks fire on a fresh
  session and not on `copilot --resume`. Measured 2026-08-31 by instrumenting the
  binary's entry point, which logged four invocations for turn 1 and none for
  turn 2, while the recorder — registered in the home's own config rather than as
  a plugin — recorded all ten hooks. A marketplace sourced from a GitHub repo is
  copied into `installed-plugins` and does fire on resume, which is what a user
  gets, so this never reached the product. `qa-session-copilot.sh` materializes a
  live install for that reason; if this shape returns, check that step 3b still
  runs, by looking for `materialized it so a resumed turn runs its hooks` in the
  driver's output.
- **verified.** 2026-08-28, signals: pass+fail. The fail half was the real defect,
  not a provoked one. Re-run 2026-08-31 against Copilot CLI 1.0.81 and the
  materializing driver: 2 `chat` and 2 `execute_tool` spans, 30087 and 30372
  input tokens per turn against a session total of 60459.

```sh
QA_COPILOT_BINARY=working-tree \
QA_COPILOT_RESUME='Now run the shell command: echo qa-second. Then reply with exactly the word done.' \
  qa/tools/qa-session-copilot.sh \
  'Run the shell command: echo qa-first. Then reply with exactly the word done.' \
  setup-probe-copilot-turns
sleep 25
qa/tools/qa-compare.py qa/runs/setup-probe-copilot-turns
qa/tools/qa-otel.py qa/runs/setup-probe-copilot-turns
```

### cursor-toolchain-present

- **proves.** A `cursor` run needs `cursor-agent`, `go`, `python3` and `git` on top
  of what the shared checks cover. A missing one surfaces after the scratch project
  is built and, with auth in place, after the session has already been paid for.
  `uuidgen` is deliberately absent from the list: this runtime discovers the session
  id from the recording rather than pinning one.
- **after.** none
- **blocking.** true
- **pass.** No output, then a version line.
- **fail.** `MISSING: <tool>`. `cursor-agent` comes from
  `curl https://cursor.com/install -fsS | bash`. `git` is needed because the driver
  creates a real repository for the session to work in, which is what
  `internal/vcs` reads.
- **verified.** 2026-09-01, signals: pass+fail

```sh
for t in cursor-agent go python3 git; do command -v "$t" >/dev/null || echo "MISSING: $t"; done
cursor-agent --version | head -1
```

### cursor-registration-is-the-shipped-wrapper

- **proves.** The hook the machine runs is the bootstrap this checkout ships. It is
  not a tidiness check: a wrapper from before 0.1.25 read the configuration file
  itself and re-exported `CURSOR_PLUGIN_OPTION_AUTH_TOKEN` from it, which is the
  same high-precedence form the driver uses — so the QA token is replaced by the
  developer's own and every export 401s while the session runs perfectly. Measured
  2026-09-01 against the v0.1.19 wrapper: 6 hooks recorded, both spans built and
  written to `plugin-debug.log`, zero spans in Dash0, and the report read as total
  telemetry loss.

  It costs nothing to run: no session, no token, no model call. It compares two
  files.
- **after.** cursor-toolchain-present
- **blocking.** true
- **pass.** `registration ok` and a version line.
- **fail.** `NO HOOKS FILE` or `NO DASH0 HOOK` — install the plugin with
  `install-cursor.sh` first. `TWO REGISTRATIONS` means `~/.cursor/hooks.json` names
  more than one `cursor-on-event.sh` path, and every span would be emitted twice;
  clean the file up. `STALE WRAPPER` means the registered file differs from
  `cursor/cursor-on-event.sh`; re-install, or copy the shipped wrapper over the
  registered path if the registration itself is fine. The driver refuses in this
  case, and `QA_CURSOR_ALLOW_STALE=1` is the deliberate override.
- **verified.** 2026-09-01, signals: pass+fail. The fail half was the real
  situation on the machine this was built on, not a provoked one; the pass half ran
  after the shipped wrapper was copied over the registered path.

```sh
python3 - <<'PY'
import filecmp, json, os, sys
path = os.path.expanduser("~/.cursor/hooks.json")
if not os.path.exists(path):
    sys.exit(f"NO HOOKS FILE: {path}")
hooks = json.load(open(path)).get("hooks") or {}
ours = sorted({e.get("command", "") for v in hooks.values() for e in v
               if "cursor-on-event.sh" in e.get("command", "")})
if not ours:
    sys.exit(f"NO DASH0 HOOK in {path}")
if len(ours) > 1:
    sys.exit(f"TWO REGISTRATIONS: {', '.join(ours)}")
script = ours[0].replace("$HOME", os.path.expanduser("~"))
if not filecmp.cmp(script, "cursor/cursor-on-event.sh", shallow=False):
    sys.exit(f"STALE WRAPPER: {script} differs from cursor/cursor-on-event.sh")
print("registration ok:", script)
print([l.strip() for l in open(script) if l.startswith("VERSION=")][0])
PY
```

### cursor-user-config-does-not-outrank-the-qa-override

- **proves.** Two keys in the developer's own
  `~/.cursor/dash0-agent-plugin.local.md` beat the QA environment, and both produce
  a healthy session whose spans go somewhere else or nowhere.
  `auth_token_keychain_service` outranks `CURSOR_PLUGIN_OPTION_AUTH_TOKEN` by
  documented design, so the session would export with that token instead of the QA
  one. `enabled: false` makes the binary return before it reads anything.

  Neither is a product defect and neither is fixable from inside a run, which is
  why the driver stops on both rather than reporting a telemetry loss it caused
  itself. This check is the same test, run before a session is paid for.
- **after.** none
- **blocking.** true
- **pass.** `user config ok`, or `no user config` when the file does not exist —
  which is also fine, since the driver supplies everything through the
  environment.
- **fail.** The message names the key. Comment it out for the run; do not delete
  it, it is the developer's own configuration.
- **verified.** 2026-09-01, signals: pass+fail. The fail half was provoked on a
  copy of the file with each key added in turn.

```sh
python3 - <<'PY'
import os, sys
path = os.path.expanduser("~/.cursor/dash0-agent-plugin.local.md")
if not os.path.exists(path):
    print("no user config"); sys.exit(0)
text = open(path).read()
if "auth_token_keychain_service" in text:
    sys.exit(f"{path}: auth_token_keychain_service outranks the QA token")
for line in text.splitlines():
    if line.replace(" ", "").startswith("enabled:") and "false" in line:
        sys.exit(f"{path}: enabled: false disables the plugin entirely")
print("user config ok")
PY
```

### cursor-driver-writes-no-project-config

- **proves.** The config lookup prefers `.cursor/dash0-agent-plugin.local.md` in
  the working directory over the one in `$HOME`, so a project-level file in the
  session's workspace would outrank the QA environment for `enabled` and for
  anything the driver does not override. This is the same trap the other three
  runtimes have, reached by a fourth door.

  It reads the driver rather than an existing run directory on purpose. A config
  file in an old run's project cannot affect a new session, so scanning `qa/runs/`
  produces failures that mean nothing.
- **after.** none
- **blocking.** true
- **pass.** `driver clean`.
- **fail.** `driver WRITES A PROJECT CONFIG` means the driver names that file under
  `$PROJECT`; remove the write. Unlike the codex and copilot drivers, this one
  writes no config file at all — it passes everything through
  `CURSOR_PLUGIN_OPTION_*` — so any match here is a regression.
- **verified.** 2026-09-01, signals: pass+fail. The fail half was provoked on a
  copy of the driver with a `$PROJECT`-scoped write appended.

```sh
grep -nE '^[[:space:]]*[^#[:space:]].*dash0-agent-plugin\.local\.md' qa/tools/qa-session-cursor.sh |
  grep -q 'PROJECT' && echo "driver WRITES A PROJECT CONFIG" || echo "driver clean"
```

### cursor-driver-ignores-an-earlier-runs-rows

- **proves.** The driver reads readiness and turn completion out of
  `record/index.jsonl`, and that file is never deleted, so a rerun under a fixed
  run id — which every check and every spec here uses — starts with a full
  recording already in place. Scoped wrong, the driver counts a previous run's
  `sessionStart`, skips waiting, types into a terminal that is not up, and exits
  `0` on a session it never drove. The report then reads clean.

  It costs nothing: no Cursor process, no token, no model call. It runs the
  driver's own `count` against a two-part fixture.
- **after.** none
- **blocking.** true
- **pass.** `baseline holds`.
- **fail.** `BASELINE IGNORED` means `count` counted a row from before the
  baseline; `rows()` in `qa-cursor-drive.py` is what to repair. `BASELINE BLIND`
  means it also missed the row appended after the baseline, which breaks every
  healthy run rather than just a rerun.
- **verified.** 2026-09-01, signals: pass+fail. The fail half was provoked on a
  copy of the driver with the `skip` argument dropped.

```sh
python3 - <<'PY'
import importlib.util, json, tempfile, os
spec = importlib.util.spec_from_file_location('drive', 'qa/tools/qa-cursor-drive.py')
drive = importlib.util.module_from_spec(spec); spec.loader.exec_module(drive)
with tempfile.TemporaryDirectory() as tmp:
    index = os.path.join(tmp, 'index.jsonl')
    with open(index, 'w') as fh:
        for event in ('sessionStart', 'afterAgentResponse', 'sessionEnd'):
            fh.write(json.dumps({'session_id': 'earlier', 'hook_event_name': event}) + '\n')
    stale = drive.count(index, 'sessionStart', 3)
    with open(index, 'a') as fh:
        fh.write(json.dumps({'session_id': 'this-run', 'hook_event_name': 'sessionStart'}) + '\n')
    live = drive.count(index, 'sessionStart', 3)
print('BASELINE IGNORED' if stale else 'BASELINE BLIND' if live != 1 else 'baseline holds')
PY
```

### cursor-recorder-covers-every-plugin-event

- **proves.** The recorder is registered for every event the plugin acts on. The
  list is generated from `cursor/hooks.json` at run time, so this asserts the
  generation rather than a copy: if the plugin grows an event, the recording must
  grow with it or it would silently stop covering the pipeline's input while still
  looking full.

  It costs nothing — no Cursor process, no token, no model call. It runs the
  driver's own generator against the shipped hooks file.
- **after.** none
- **blocking.** true
- **pass.** `covers every plugin event`, then the event list.
- **fail.** `MISSING: <event>` means an event in `cursor/hooks.json` is absent from
  the generated registration; the generator in `qa-session-cursor.sh` is what to
  repair.

  Note what this check deliberately does **not** flag. `preToolUse` is registered,
  and on Cursor that is correct: the plugin registers it too, and the recorder
  writes nothing to stdout and exits 0. The Copilot equivalent forbids it, because
  there it is the one fail-closed event. Do not carry that rule across.
- **verified.** 2026-09-01, signals: pass+fail. The fail half was provoked by
  removing `afterAgentResponse` from a copy of `cursor/hooks.json`.

```sh
python3 - <<'PY'
import json, re, subprocess
generator = re.search(r"cursor/hooks\.json\".*?<<'PY'\n(.*?)\nPY\n",
                      open('qa/tools/qa-session-cursor.sh').read(), re.S).group(1)
out = subprocess.run(['python3', '-c', generator, 'cursor/hooks.json', '/tmp/qa-recorder-probe'],
                     capture_output=True, text=True, check=True).stdout
registered = json.loads(out)['hooks']
missing = [e for e in json.load(open('cursor/hooks.json'))['hooks'] if e not in registered]
print('MISSING: ' + ', '.join(missing) if missing else 'covers every plugin event')
print(', '.join(registered))
PY
```

### cursor-probe-session-agrees-with-what-it-was-fed

- **proves.** The whole method on a Cursor session small enough to reason about:
  the recorder saw every hook, the machine's registration exported to the QA
  target, Dash0 stored it, and the span counts, tool names and token counts agree
  with the hooks. This runtime needs no equivalent of
  `qa-reads-the-environment-the-plugin-writes-to`, because the driver's
  `CURSOR_PLUGIN_OPTION_*` overrides make the write side and the read side the same
  place by construction — which means a mismatch cannot be the explanation when
  this fails, and something real is.
- **after.** cursor-registration-is-the-shipped-wrapper,
  cursor-user-config-does-not-outrank-the-qa-override,
  ingest-token-reaches-the-ingress, token-reads-the-dataset
- **blocking.** true
- **pass.** `All three records agree.` and exit `0` from `qa-compare.py`, then exit
  `0` from `qa-attrs.py`.
- **fail.** Exit `1` prints each difference. The `hooks` column is a real
  expectation on this runtime — for `chat`, `execute_tool` and `invoke_agent` alike
  — so a span Dash0 lacks that the hooks imply is the plugin's or the transport's
  fault. The token column reads `-`: the transcript carries no number, and that is
  not a channel failure. Exit `2` means a channel was unavailable.

  Two failures are specific to this runtime. `no transcript.jsonl in the run` means
  the driver found no transcript, so the turn count has no second reading rather
  than being zero. And a span count of zero **with** a non-zero `spans_logged` in
  the manifest means the plugin built the spans and they were lost after it — which
  on this runtime is almost always the stale wrapper handing over the wrong token,
  so re-run `cursor-registration-is-the-shipped-wrapper` before anything else.

  **Wait 25 seconds, not 8.** See `## Settling`.
- **verified.** 2026-09-01, signals: pass-only for the green path. The whole path
  ran against cursor-agent 2026.08.31-4057e58: 6 hooks recorded, 1 `chat` and 1
  `execute_tool` in Dash0, parented, and the four token counts equal to the
  `afterAgentResponse` payload's to the token.

  `qa-attrs.py` exited `1` on the first run, on five real findings rather than a
  setup problem — see the preamble to `## Checks`. All five are fixed and the
  re-run exits `0` with 37 observed keys instead of 41. Both tools exiting `0` is
  the pass signal now.
- **shape.** Measured on the probe below: 6 hook invocations — `sessionStart`,
  `beforeSubmitPrompt`, `preToolUse`, `postToolUse`, `afterAgentResponse`,
  `sessionEnd` — 3 distinct transcript snapshots, 2 spans in Dash0, and
  `"spans_logged": 2`.

```sh
QA_CURSOR_BINARY=working-tree qa/tools/qa-session-cursor.sh \
  'Run the shell command: echo qa-probe. Then reply with exactly the word done.' \
  setup-probe-cursor
sleep 25
qa/tools/qa-compare.py qa/runs/setup-probe-cursor
qa/tools/qa-attrs.py qa/runs/setup-probe-cursor
```

### cursor-resumed-turn-is-scoped-to-itself

- **proves.** The per-turn boundary, which is the one thing a single-turn probe
  cannot see: with one turn, "this turn's usage" and "the session's usage" are the
  same number and a double-count is invisible. Both other multi-turn runtimes
  shipped a defect exactly there.

  Cursor's mechanism makes that failure mode unreachable — usage arrives per turn
  in the `afterAgentResponse` payload, already scoped by the host, so there is no
  cursor to keep and no file to re-read. What this check proves is that the plugin
  does not *un*-scope it, by summing or by failing to clear the trace context
  between turns.

  It is also the cheapest multi-turn run of any runtime. The TUI stays open, so
  `QA_CURSOR_RESUME` is a second prompt typed into the running session: no resume
  flag, no relaunch, and none of the questions that a Copilot resume brought with
  it.
- **after.** cursor-probe-session-agrees-with-what-it-was-fed
- **blocking.** false. Without it, a cursor run says nothing about per-turn
  scoping, and a spec that needs it ships single-channel.
- **pass.** Exit `0`, 2 `chat` and 2 `execute_tool` spans in Dash0 in **two**
  traces, and each turn's input tokens close to the other's rather than double.
- **fail.** Token counts roughly double the per-turn payloads, or three
  `execute_tool` spans for two tool calls, is the failure the other runtimes had.
  Two turns landing in **one** trace is a different failure: the trace context was
  not cleared at `Stop`.

  A turn that ran no tool is a failed run of this check rather than a passing one;
  re-run rather than asserting over 2 spans.
- **verified.** 2026-09-01, signals: pass-only. Measured on
  `qa/runs/setup-probe-cursor-turns`: 10 hooks, 4 spans, turn 1 at 32283 input and
  turn 2 at 32650 against a session total of 64933 — each turn its own, and neither
  span carrying the total. Provoking the failure would mean breaking the scoping on
  purpose, which this check exists to notice rather than to cause.

```sh
QA_CURSOR_BINARY=working-tree \
QA_CURSOR_RESUME='Now run the shell command: echo qa-second. Then reply with exactly the word done.' \
  qa/tools/qa-session-cursor.sh \
  'Run the shell command: echo qa-first. Then reply with exactly the word done.' \
  setup-probe-cursor-turns
sleep 25
qa/tools/qa-compare.py qa/runs/setup-probe-cursor-turns
qa/tools/qa-transcript-cursor.py qa/runs/setup-probe-cursor-turns
```
