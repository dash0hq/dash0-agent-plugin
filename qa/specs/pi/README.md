# Pi specs

## The three things to know before reading any spec here

**The thing under test is this working tree, not an install.** Pi loads the
plugin in-process as an extension, and the driver loads `pi/index.ts` with
`--no-extensions -e`, next to a helper it builds into the run directory. So a
run always measures the current source, and the machine's own Pi extensions,
including an installed copy of this plugin, never load and cannot export twice.
Pi's own login is the only thing the run takes from the machine.

**The second channel is Pi's own `--mode json` stream, and it is independent.**
The plugin receives Pi's events in-process. The stream is what Pi writes to
stdout for the same session, and the plugin never reads it. Pi normalizes usage
for every provider, so the stream carries per-call token counts, which makes
token totals an exact check here, not only a scoping check. A recorder
extension, loaded before the plugin, writes every event the plugin was given to
`record.jsonl`, so a wrong span can be told apart from a wrong input.

**Pi reads its own dataset key.** The driver and the comparer use `piDataset`
from `qa/config.local.json`, not `dataset`, so QA spans stay out of the shared
dataset. `qa-attrs.py` needs `--dataset <piDataset>` explicitly; its default
reports a clean pass over zero spans.

## What a run here can and cannot prove

| Area | State |
| --- | --- |
| [session](session/) | Turn shape, one `chat` span per model call, tool spans, a four-turn conversation with a failing tool. Solid. |
| [usage](usage/) | Per-call tokens reconcile exactly with the stream. The provider comes from each call; verified for `claude-code` and `openai-codex`. |
| [mcp](mcp/) | Prefix stripping, per-server naming, and failure status. Solid. |
| [skills](skills/) | Both routes: the model reading `SKILL.md`, and a `/skill:name` command. Solid. |
| subagents | Not applicable. Pi has no built-in sub-agent; extensions that start one run a separate `pi` process, which is a separate session. |
| aborted turns | Not covered by a spec. `-p` cannot abort a turn on demand; `test/e2e` and the extension tests cover the abort path. |

Every spec here was run against Dash0 on 2026-11-13 and agreed. The attribute
surface was checked with `qa-attrs.py` on the four-turn conversation run: every
key is documented.

## Spans can arrive minutes after the run

The helper is detached and exports at once, but Dash0 can take minutes to make
a batch queryable. `qa-pi-compare.py` polls for the turn roots for up to six
minutes, so a spec needs no fixed settle time. A read that reports "no spans"
before that is a statement about the backend, not about the plugin.
