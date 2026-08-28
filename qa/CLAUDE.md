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

## Three runtimes

Specs target Claude Code, Codex or GitHub Copilot CLI, and say which in their `runtime:`
frontmatter. Each has its own driver, its own second channel, and its own limits on what a run can
prove. `## Runtimes` in [setup.md](setup.md) is the table; read it before running or writing
anything, and never carry a result from one runtime over to another.

Copilot is the one that differs in kind rather than in detail. Its hooks carry no numbers and no
tool events the plugin uses, so its second channel — Copilot's own OpenTelemetry file — is also the
plugin's input. Agreement there proves a faithful copy, not a correct measurement. Read
`## The one thing to know before reading any spec here` in [specs/copilot](specs/copilot/README.md)
before writing or judging a Copilot spec.

## Findings

Report spec failures that are unaddressed in `findings/`
