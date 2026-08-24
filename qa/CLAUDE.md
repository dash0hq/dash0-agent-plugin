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

## Findings

Report spec failures that are unaddressed in `findings/`
