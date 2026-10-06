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

**Amp writes to its own dataset, not `default`.** Both the driver and every
verification tool must be pointed at it — `qa-attrs.py` needs
`--dataset <ampDataset>` explicitly — the `ampDataset` value from
`qa/config.local.json` — and its default would report a clean pass over zero
spans. See `### Amp writes to its own dataset, not default` in
[../../setup.md](../../setup.md).

## What a run here can and cannot prove

| Area | State |
| --- | --- |
| [session](session/) | Turn shape, multi-turn threads, tool status, attribute surface. Solid. |
| [mcp](mcp/) | Prefix stripping, per-call server naming, failure status. Solid, and the only place the tool-error path is reachable. |
| [skills](skills/) | Solid, after a fix. |
| [tools](tools/) | Server-executed tools reach no span at all. One open finding, and it stays open. |
| [usage](usage/) | Opt-in, and correct after a fix — every model call attributed, tokens reconciling against the stream. |
| [subagents](subagents/) | Not applicable — Amp exposes no spawn tool. |
| orb executor | Not runnable. `QA_AMP_EXECUTOR=orb` exists in the driver, but nothing copies a hand-built local plugin install into an orb. |

## Spans arrive after the run finishes

With usage export on, the helper polls `amp threads export` for up to twenty
seconds, and the bridge detaches it rather than making the turn wait — Amp
awaits the `agent.end` handler, so anything waited for there is latency the
user feels between turns. **A turn's spans can therefore land after `amp` has
exited.**

Every spec here settles for 30 s, not 10 s, whenever `QA_AMP_USAGE=1`. A read
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
