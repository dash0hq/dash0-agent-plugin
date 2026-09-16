# CLAUDE.md — qa/

## Driven by the engineering plugin

The QA skills in the `engineering` plugin own this directory: `qa-setup`, `qa-run`, `qa-author`,
`qa-learn`. [setup.md](setup.md) is the adapter they read, and it is the only file to change when
the project moves.

| Command | Does | Writes to |
| --- | --- | --- |
| `/qa-setup` | Preflight, or repair a stale check. User-invoked. | `setup.md` |
| `/qa-run <spec>`, `/qa-run --explore <area>` | Executes or explores. | the runs directory |
| `/qa-author` | Writes specs. User-invoked. | the specs directory |
| `/qa-learn` | Records what a run taught. | the learnings directory |

One writer per directory, so a hand edit in the wrong place gets overwritten or quietly ignored.

## Five runtimes

Specs target Claude Code, Codex, GitHub Copilot CLI, Cursor or Amp, and say which in their
`runtime:` frontmatter. Each has its own driver, its own second channel, and its own limits on what
a run can prove. `## Runtimes` in [setup.md](setup.md) is the table; read it before running or
writing anything, and never carry a result from one runtime over to another.

Two of them differ in kind rather than in detail, and each has a page to read before writing or
judging one of its specs.

**Copilot.** Its hooks carry no numbers and no tool events the plugin uses, so its second channel —
Copilot's own OpenTelemetry file — is also the plugin's input. Agreement there proves a faithful
copy, not a correct measurement. Read `## The one thing to know before reading any spec here` in
[specs/copilot](specs/copilot/README.md).

**Cursor.** Its headless mode fires no `afterAgentResponse`, so the driver types into an interactive
terminal and reads turn completion out of the recording. And its second channel, Cursor's own
transcript, carries no token count at all: usage exists only in the hook payload, which is the
plugin's input. So a cursor run can prove that a token count is *scoped* correctly and never that it
is correct.  Read `## The two things to know before reading any spec here` in
[specs/cursor](specs/cursor/README.md).

**Amp** has no shell hooks at all: the plugin is a TypeScript bridge Amp loads in-process, so the
recorder is a second Amp plugin rather than a wrapper, installed project-scoped into
`.amp/plugins/qa-recorder/`. Its second channel is Amp's own `--stream-json`, which is genuinely
independent and carries per-call usage. `amp threads export` is **not** a second channel: the plugin
reads it itself. Amp writes to its own dataset (`ampDataset` in the config), not `default`, so
every verification tool must be pointed at it — `qa-attrs.py` needs `--dataset <ampDataset>`
explicitly, and its default reports a clean pass over zero spans. Amp exposes no subagent spawn tool, and the orb executor is
not runnable yet. And since the usage fix the helper is **detached**: it polls for up to twenty
seconds and a turn's spans can arrive after `amp` has exited, so amp runs settle for 30 s, not 10 s.
Read [specs/amp](specs/amp/README.md).

## Findings

Report spec failures that are unaddressed in `findings/`. Two are open today:
[cursor-subagent-work-produces-no-span](findings/cursor-subagent-work-produces-no-span.md) and
[amp-server-side-tools-produce-no-span](findings/amp-server-side-tools-produce-no-span.md).
The second is a limit of Amp's plugin API, not a bug — it stays open and documented.

Two amp findings were fixed on 2026-09-15 and are kept for their measurements:
[amp-answering-model-call-is-never-attributed](findings/amp-answering-model-call-is-never-attributed.md)
and [amp-skill-invocation-is-not-attributed](findings/amp-skill-invocation-is-not-attributed.md).
The skill fix touched shared `internal/pipeline` code, and this machine's token
cannot reach the other four runtimes' datasets, so their skill specs are still
owed a re-run somewhere that can.
