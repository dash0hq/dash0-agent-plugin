# Amp specs

## The three things to know before reading any spec here

**The plugin is a TypeScript bridge Amp loads in-process.** There is no shell
wrapper to intercept and no hook payload on disk, so the recorder is a *second
Amp plugin* bound to the same four events, installed project-scoped into
`.amp/plugins/qa-recorder/`. It writes each event verbatim before the bridge
acts on it. That is what lets a run tell "the bridge was fed the wrong thing"
apart from "the bridge did the wrong thing with it" — a distinction no amount
of span reading can make, and the one that found two of the three open
findings.

**The second channel is Amp's own `--stream-json`, and it is genuinely
independent.** `amp threads export` is **not**: the plugin reads it itself to
build usage, so agreement there would prove a faithful copy rather than a
correct measurement. It is read only to explain a usage mismatch after one has
been found.

**Amp reads its own dataset key.** Both the driver and every verification tool
must be pointed at `ampDataset` from `qa/config.local.json` — `qa-attrs.py`
needs `--dataset <ampDataset>` explicitly — because a tool's own default can
report a clean pass over zero spans. See `### Amp reads its own dataset key` in
[../../setup.md](../../setup.md) for what that value is today and why.

## What a run here can and cannot prove

| Area | State |
| --- | --- |
| [session](session/) | Turn shape, multi-turn threads, tool status, attribute surface, and how many `chat` spans a turn emits. Solid. |
| [mcp](mcp/) | Prefix stripping, per-call server naming, failure status. Solid, and the only place the tool-error path is reachable. The call *count* is no longer independently witnessed: current Amp runs MCP tools inside `code_exec`, which fires no hook. |
| [skills](skills/) | Solid, after a fix. |
| [tools](tools/) | Tool names are copied verbatim. Server-executed tools, and now `code_exec`/`tool_search`, reach no span at all — two open findings, and they stay open. |
| [usage](usage/) | On by default, and correct — every model call attributed, tokens reconciling exactly against the stream. The opt-out is covered too. |
| [subagents](subagents/) | Not applicable — Amp exposes no spawn tool. |
| orb executor | Not runnable. `QA_AMP_EXECUTOR=orb` exists in the driver, but nothing copies a hand-built local plugin install into an orb. |

## Spans arrive after the run finishes, sometimes minutes after

**Measured 2026-10-12: a batch Dash0 answered `200` took between 2.5 and 5.5
minutes to become queryable, while four runs in the same hour were readable in
30 s.** `qa-amp-compare.py` now waits for the turn roots rather than reading
once, so a spec stays autonomous. A single read that reports "no spans" is a
statement about the backend's mood, not about the plugin.



With usage export on, the helper polls `amp threads export` for up to twenty
seconds, and the bridge detaches it rather than making the turn wait — Amp
awaits the `agent.end` handler, so anything waited for there is latency the
user feels between turns. **A turn's spans can therefore land after `amp` has
exited.**

Every spec here settles for 30 s, not 10 s, because usage export is on by
default (`QA_AMP_USAGE=0` is the only way off). A read
taken too early reports `dash0.amp.usage.status=partial`, or no spans at all,
for a run that is completely healthy — which looks exactly like the defect this
replaced. Check the settle time before believing a regression.

## Never skip the staleness check

`amp-installed-plugin-matches-the-tree` in [../../setup.md](../../setup.md)
must pass before any spec here. The installed plugin is a hand-built directory
copy that no `git checkout` updates, so without it a run silently measures an
old build. The check compares binaries built with `-buildvcs=false` on both
sides; without that flag Go stamps `vcs.revision` and identical source reports
`STALE`.
