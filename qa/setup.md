---
qa_root: qa
app_kind: plugin
config_file: qa/config.local.json
last_full_pass: 2026-08-21
---

# QA setup for dash0-agent-plugin

A QA run drives a real Claude Code session against the plugin as it is actually
installed, lets it export to the real Dash0 target it is configured for, and then
reads the spans back with `dash0 spans query`. Alongside the plugin's own hooks it
registers a recorder that captures every hook payload and a snapshot of the
transcript as it stood at that moment. That recording is the pipeline's entire
input, so the expectation can be computed without the plugin's involvement, and
each recorded pair is a replayable unit-test fixture.

The target is a shared, non-disposable dataset that a QA run does not own. So
nothing in a run may be destructive, and every read must filter by
`gen_ai.conversation.id`: an unfiltered query returns other sessions, and a
count taken without that filter is meaningless. QA spans cannot be deleted
afterwards either; they stay in the dataset. Treat anything a query returns that
is not this run's own session as none of QA's business, and keep it out of every
spec, learning, finding, and report.

Only Claude Code is covered. Cursor, Codex, and Copilot share the Go pipeline but
have different hook shapes and different usage sources, and neither the recorder's
transcript logic nor the audit script transfers to them. `test/contracts/` and
`test/e2e/` cover those runtimes as far as they can be covered without a host.

## Layout

- specs:     qa/specs/<area>/
- learnings: qa/learnings/
- runs:      qa/runs/            (gitignored)
- fixtures:  qa/recorder/, qa/tools/
- config:    qa/config.local.json (gitignored), qa/config.local.json.example

## Configure

**The thing under test is not configured by QA.** The installed plugin runs with
its own configuration, which on a Dash0 machine comes from
`~/.claude/remote-settings.json` under
`pluginConfigs/dash0-agent-plugin@dash0/options` and arrives as
`CLAUDE_PLUGIN_OPTION_*`. `qa/tools/qa-session.sh` adds one thing to the session:
a second hook handler, registered in a scratch project's `.claude/settings.json`,
generated from `claude/hooks.json` so it cannot miss an event the plugin acts on.

**What is configured is the read side.** `qa/config.local.json` holds the API
endpoint, the token, and the dataset the comparison reads back from. Copy the
example and fill it in. Ask the team for the values; they are the same ones the
`dash0` repository's QA setup uses. Never invent a token, a URL, or a tenant
name, and never lift one out of shell history or an earlier transcript.

```sh
cp qa/config.local.json.example qa/config.local.json
chmod 600 qa/config.local.json
```

| Key | Meaning | Sharp edge |
| --- | --- | --- |
| `apiUrl` | Where spans are read from | Must be the API host, not the ingress host. The two differ only in a hostname prefix, and pointing at the wrong one fails as a connection error rather than an auth error. |
| `appUrl` | UI base for a session link | Only used to build a human link. `internal/sessionurl/sessionurl.go` derives the same value from the ingress host, so a mismatch here means a report links somewhere the spans are not. |
| `ingestUrl` | Where the plugin is expected to write | Not used to send anything. It exists so a check can prove QA reads the environment the plugin writes to. |
| `authToken` | Reads spans through the API | A live token. Never needs write scope: QA never ingests, the plugin does. |
| `dataset` | The dataset to read | Must be the installed plugin's `DATASET`, which is `default`, not `qa`. Reading a *different readable* dataset returns an empty result that looks exactly like the plugin having sent nothing. |
| `org` | Organization slug | Informational. |

The `dash0` CLI's own active profile is deliberately not used. It carries its own
dataset, which on this machine resolves to one the token cannot read, and every
QA command therefore passes `--api-url`, `--auth-token`, and `--dataset`
explicitly.

> [!CAUTION]
> `authToken` is a live token against a shared environment. It never goes into a
> ticket, a message, a commit, or a screenshot. `qa-compare.py` strips it from
> any command it prints, and `run-dir-carries-no-real-credential` checks that no
> run directory picked one up.

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

### Testing an unreleased change

By default a run tests the installed release, because that is what the machine
actually runs. `QA_SWAP_BINARY=1` builds the working tree over the installed
binary cache for the duration of the run and restores it on exit, including on
failure. It is opt-in because that cache is shared with the developer's own live
sessions.

## Stimulate

```sh
qa/tools/qa-session.sh "<prompt>" [run-id]
qa/tools/qa-compare.py qa/runs/<run-id>
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

## Observe

1. **Dash0** — `dash0 spans query` with the endpoint, token, and dataset from
   `qa/config.local.json`, filtered to `gen_ai.conversation.id`. This is the product's output
   as a consumer sees it, at full precision. `--precision disabled` is mandatory:
   adaptive sampling drops spans, and a dropped span reads as a span the plugin
   never sent. JSON output is capped at 100 records, so `qa-compare.py` warns when
   a result hits its limit rather than reporting a floor as a total.
2. **`claude-result.json`** — Claude Code's own in-process usage and cost. Good
   at cost, which no span carries. Bad at sub-agents: it reports the main
   session's usage only, so a session with a sub-agent shows numbers far below
   both Dash0 and the transcript. That gap is expected and is not a finding.

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

## Settling

Ingest lag only, and it is short: spans for a session are queryable a few seconds
after it ends. `qa-compare.py` widens the query window by 60 seconds before the
run's start and 120 seconds after its end, which has been enough in every run so
far. A comparison that reports zero spans immediately after a session should be
re-run before it is believed.

There is no settling inside a session. Every hook exports synchronously before
its process exits.

## Checks

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
- **pass.** `ignored`, and both tool paths print `tracked`.
- **fail.** `NOT ignored` for `qa/runs` means the `qa/runs/` line is gone from
  `.gitignore`; restore it before running anything. `IGNORED` for a tool path is
  the opposite problem: `.gitignore` has a bare `bin/` rule that matches any
  directory named `bin` at any depth, which is why these live in `qa/tools/` and
  must not move to `qa/bin/`.
- **verified.** 2026-08-21, signals: pass+fail

```sh
git check-ignore -q qa/runs && echo ignored || echo "NOT ignored"
for p in qa/tools/qa-session.sh qa/tools/qa-compare.py; do
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
  payload and a full transcript, and there are now two live tokens on this
  machine that could end up in one: `.env`'s ingest token, and
  `qa/config.local.json`'s read token, which `qa-compare.py` passes on a command
  line.
- **after.** config-is-complete
- **blocking.** true
- **pass.** `control ok` for each token, then `clean` for every scan. The control
  halves matter: a grep that matches nothing anywhere proves nothing about the run
  directories.
- **fail.** Any path printed is a leaked credential. Delete that run directory and
  find out how the value got there before running again. `control missing` means
  the check tested nothing, so fix the check rather than trusting it.
- **verified.** 2026-08-21, signals: pass+fail

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
