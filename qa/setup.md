---
qa_root: qa
app_kind: plugin
config_file: qa/config.local.json
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

Two of the four supported agents are covered here, **one spec tree per runtime**:
`qa/specs/claude/` and `qa/specs/codex/`, each split by topic underneath. Each spec also names its runtime in
frontmatter, so the area and the field cannot drift apart. The split is by runtime
rather than by topic because a run is one driver, one credential and one cost
profile — `/qa-run codex` is a coherent thing to execute, while a topic area
spanning both would need two drivers mid-run. A spec written for one runtime says
nothing about the other. They share the Go pipeline and therefore share most
invariants, but the two differ in what a run can prove:

| | claude | codex |
| --- | --- | --- |
| Driver | `qa/tools/qa-session.sh` | `qa/tools/qa-session-codex.sh` |
| What is under test | the plugin **as this machine has it installed** | the shipped install path, **provisioned into a throwaway home** |
| Who configures it | the managed install; QA cannot | QA, from `qa/config.local.json` |
| Second channel | the transcript, via `claude-code-usage-audit.py` | the rollout, via `qa/tools/qa-rollout.py` (usage only) |
| Harness's own figures | `claude -p --output-format json`, including cost | `codex exec --json`; Codex reports no cost |
| Sees what was sent | no | yes, through the plugin's debug log |
| Touches the machine | yes: the binary cache, under `QA_SWAP_BINARY=1` | no |

The asymmetry is not a preference, it is what each host allows. Claude Code's
options arrive from a managed `remote-settings.json` that beats every override,
so QA has to take the install as it finds it. Codex has no managed layer at all,
so QA provisions one and gets a hermetic run in exchange.

**What each runtime therefore cannot answer.** A `claude` run cannot see the bytes
the plugin sent, so questions about the wire belong in `test/e2e/`. A `codex` run
cannot tell you whether the Codex install on this machine is configured correctly,
because it does not use it — and it cannot answer that for anyone else's machine
either. Neither runtime's result carries over to the other: a fix verified on
`claude` is unverified on `codex` until a `codex` spec says otherwise.

Cursor and Copilot are not covered. `test/contracts/` and `test/e2e/` cover them
as far as they can be covered without a host.

## Layout

- specs:     qa/specs/<runtime>/<topic>/   (`claude/session`, `codex/subagents`, ...)
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
| `ingestUrl` | Where the plugin is expected to write | For `claude`, nothing is sent here: it exists so a check can prove QA reads the environment the plugin writes to. For `codex`, this is where the provisioned install actually exports. |
| `authToken` | Reads spans back, and for `codex` also ingests them | A live token, and it must do **both**. The `claude` runtime only reads, so a read-scoped token is enough there. The `codex` runtime provisions the install and hands this same token to the plugin, and a token that cannot ingest 401s on every export — a run that looks perfectly healthy and reports zero spans. `codex-auth-token-can-ingest` proves it before a session is paid for. |
| `dataset` | The dataset to read, and for `codex` to write | Must be the installed plugin's `DATASET`, which is `default`, not `qa`. Reading a *different readable* dataset returns an empty result that looks exactly like the plugin having sent nothing. |
| `org` | Organization slug | Informational. |

The `dash0` CLI's own active profile is deliberately not used. It carries its own
dataset, which on this machine resolves to one the token cannot read, and every
QA command therefore passes `--api-url`, `--auth-token`, and `--dataset`
explicitly.

> [!CAUTION]
> `authToken` is live, against a shared environment, and on the `codex` runtime it
> can write as well as read. It never goes into a ticket, a message, a commit, or
> a screenshot. `qa-compare.py` strips it from any command it prints, and
> `run-dir-carries-no-real-credential` checks that no run directory picked it up.

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
   as absent when it finds none, never as zero.

**The `codex` runtime has one channel the other cannot have.**
`plugin-debug.log` is every span the plugin emitted, logged before the wire. It
is the product's own output, not an independent record, so it never supplies an
expectation. What it does is split one failure in two: a span in the log but not
in Dash0 was built and lost in transport or ingest, and a span in neither was
never built. On `claude` those two are indistinguishable from outside.

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

## Settling

Ingest lag only, and it is short: spans for a session are queryable a few seconds
after it ends. `qa-compare.py` widens the query window by 60 seconds before the
run's start and 120 seconds after its end, which has been enough in every run so
far. A comparison that reports zero spans immediately after a session should be
re-run before it is believed.

There is no settling inside a session. Every hook exports synchronously before
its process exits. That holds for both runtimes: `codex exec` is synchronous and
the plugin's Codex hooks POST before their process exits, so the debug log is
complete the moment the command returns, and only Dash0 lags.

## Checks

Last full pass 2026-08-25, both runtimes, against plugin 0.1.25, `claude`
2.1.238 and codex-cli 0.149.1. Every check below ran green that day, including
the two probe sessions and the binary swap. The one thing that did not pass on
the first attempt was `qa-attrs.py` on the Codex probe, which found a real
defect; it is fixed and the re-run is clean.

Checks with no prefix apply to both runtimes. A `codex-` prefix means the check
belongs to that runtime alone; skip it when a run targets `claude`, and skip the
`claude`-only ones the same way. The runtime-specific blocking checks are
`probe-session-agrees-with-what-it-was-fed` for `claude` and
`codex-probe-session-agrees-with-what-it-was-fed` for `codex`.

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
         qa/tools/qa-codex-hooks/main.go; do
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
  `authToken`, which `qa-compare.py` passes on a command line and the `codex`
  driver hands to a real install.
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

### codex-auth-token-can-ingest

- **proves.** The token actually reaches the ingress, which the `codex` runtime
  needs and the `claude` runtime does not. QA hands it to a provisioned install,
  and a token the ingress rejects 401s on every export while the session itself
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
- **verified.** 2026-08-26, signals: pass+fail. The malformed body is what keeps
  this free of side effects: it exercises authentication without ingesting a span
  into a shared dataset. Confirmed end to end as well, by running a Codex session
  whose provisioned install was given this token and reading its 2 spans back.

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
- **after.** codex-auth-token-can-ingest,
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
