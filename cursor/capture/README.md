# Cursor hook payload capture

Scaffolding to record real Cursor hook payloads so we can write the
`internal/source/cursor` adapter against ground-truth shapes rather than
the handoff brief's prose.

## Setup

1. Install Cursor.
2. Copy the capture `hooks.json` into Cursor's user-scope config:

   ```bash
   mkdir -p ~/.cursor
   cp cursor/capture/hooks.json ~/.cursor/hooks.json
   ```

   > If you already have a `~/.cursor/hooks.json` with other hooks, merge by
   > hand — Cursor's hooks config does not deep-merge across scopes the way
   > you might expect, and we don't want to clobber anything you set up.
3. Make the capture script executable:

   ```bash
   chmod +x cursor/capture/capture.sh
   ```
4. Restart Cursor so it picks up the new hooks file.

## Run

Open Cursor on this repo (or anywhere) and exercise as many of these as you
can in one session:

- **Prompt + response** — type any prompt and let the agent reply.
- **File tool calls** — ask the agent to read a file, then edit a file.
- **Shell** — ask the agent to run a shell command.
- **MCP** — invoke any MCP server you have configured.
- **Subagent** — ask the agent to delegate a sub-task (Task tool).
- **Compaction** — if you have a long session that compacts, that's also useful.

Each hook invocation writes a JSON file under:

```
cursor/captured/<timestamp>_<event_name>.json
```

If you want to direct captures elsewhere, set `DASH0_CURSOR_CAPTURE_DIR`
before launching Cursor.

## What we'll look at

For each captured payload:

1. Confirm the field names match the handoff brief (`conversation_id`,
   `generation_id`, `tool_use_id`, `tool_input`, `tool_output`, …).
2. For events that carry `transcript_path`, open the transcript file and
   inspect its format — specifically, whether it contains per-API-call
   token usage (input_tokens / output_tokens / cache_*). If it does, the
   Cursor source can read tokens the same way `internal/transcript` does
   today. If not, v1 ships without per-turn token data.
3. Verify hook firing order across a real turn: which events fire and in
   what sequence. This anchors the trace-context lifecycle in the
   Cursor adapter.

## Teardown

Remove or rename `~/.cursor/hooks.json` to stop capturing. The
`cursor/captured/` directory is git-ignored.
