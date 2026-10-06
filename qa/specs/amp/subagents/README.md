# Amp subagents — not applicable

There are no specs here, and that is a determination rather than a gap in
coverage.

**Amp exposes no tool that spawns a subagent.** `amp tools list` reports 15
built-in tools:

```
apply_patch  download_thread_changes  download_thread_file  find_thread
load_plugin  read_web_page  reload_mcp  reload_plugins  reload_skills
shell_command  shell_command_kill  shell_command_status  skill
upload_thread_file  web_search
```

None of them is a `Task`, `spawn`, or delegate equivalent. The thread tools
(`find_thread`, `download_thread_*`, `upload_thread_file`) read and write
*other existing threads*; they do not start an agent and wait for it, so they
produce no parent/child relationship for a trace to carry.

The other four runtimes each have specs here because each has a spawn tool and
each gets it at least partly wrong — see
[../../cursor/subagents](../../cursor/subagents/README.md), whose subagent work
reaches no span at all. There is no amp equivalent to get wrong.

**Re-check this before trusting it.** The tool list is Amp's and it moves. If
`amp tools list` grows a spawn tool, the claude and codex subagent specs are
the ones to port, and `internal/source/amp/amp.go` emits only `chat` and
`execute_tool` today — there is no `invoke_agent` path in it to exercise.
