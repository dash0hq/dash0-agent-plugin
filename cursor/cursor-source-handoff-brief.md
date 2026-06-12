# Handoff Brief: Add a Cursor source to dash0-agent-plugin

**Repo:** `~/source/dash0-agent-plugin`
**Audience:** Claude Code (working in the terminal with repo access)
**Purpose:** Carry over context from an external research session and extend the existing plugin to capture telemetry from Cursor in addition to Claude Code.

---

## 1. Context

We already run a **Claude Code plugin** that hooks into agent conversations and emits **OpenTelemetry spans + metrics** to a collector. We deliberately do **not** rely on Claude Code's native OTel exporter, because it lacks the conversation-level metadata we need for the insights we want (it's strong on token/cost, weak on content).

**Goal:** turn this into a multi-source agent-observability plugin. The next source to add is **Cursor**. After Cursor, we want other CLIs (Gemini CLI, Codex CLI) and IDE/SaaS-hosted tools.

The three data needs that must be satisfied per source:

1. Entered **prompts & responses**
2. **MCP calls** made by the agent
3. **Tool calls** made by the agent

---

## 2. Key finding: Cursor's integration surface is Cursor Hooks

Cursor exposes a **Hooks** system (GA since v1.7) that is the direct analog to the Claude Code hooks/plugin model.

- It is **not** OTel and **not** the Admin/Analytics API. The Admin API only returns aggregate rollups (active users, request counts, acceptance rates) — **no** prompt content, tool calls, or MCP calls. For our three data needs, **hooks are the only first-party event-level source.**
- Hooks are **spawned processes that communicate over stdio with JSON in both directions**, firing before/after stages of the agent loop. They can observe, block, or modify behavior. For telemetry we use the observational (`after*`) hooks and read-only `before*` hooks, leaving `failClosed` at its default (fail-open) so a broken exporter never breaks the user's agent loop.
- Config lives in `hooks.json` at four scopes with precedence **Enterprise → Team → Project → User**:
  - User: `~/.cursor/hooks.json`
  - Project: `<repo>/.cursor/hooks.json` (version-controlled)
  - Enterprise (MDM): `/Library/Application Support/Cursor/hooks.json` (macOS), `/etc/cursor/hooks.json` (Linux/WSL), `C:\ProgramData\Cursor\hooks.json` (Windows)
  - Team: Enterprise cloud-distributed via the Cursor web dashboard
- Cursor can also **load third-party Claude Code hooks** for compatibility, and the payload shapes are close enough that our normalization layer can largely be shared across both clients.

**Common envelope** (every hook receives these — these are our correlation keys):

| Field | Meaning |
|---|---|
| `conversation_id` | Stable across many turns → **trace root** |
| `generation_id` | Changes with every user message → **turn span** |
| `model` | Model for the composer that triggered the hook |
| `hook_event_name` | Which hook fired |
| `cursor_version` | App version |
| `workspace_roots` | Workspace folders |
| `user_email` | Authenticated user (nullable) |
| `transcript_path` | Path to full conversation transcript file (null if transcripts disabled; also in env as `CURSOR_TRANSCRIPT_PATH`) |

---

## 3. Data-need → Cursor hook mapping

### 3.1 Prompts & responses
- `beforeSubmitPrompt` → `prompt` (user text) + `attachments[]` (`type: file|rule`, `file_path`). Fires after send, before backend request; can block.
- `afterAgentResponse` → `text` (assistant final message text).
- `afterAgentThought` → `text` (aggregated thinking block) + `duration_ms`.
- Full history beyond per-turn text → read `transcript_path`.

### 3.2 MCP calls
- `beforeMCPExecution` → `tool_name`, `tool_input` (JSON params string), and **server identity** (`url` for remote, or `command` for local). Can block; recommend `failClosed: true` only if used for governance, not for telemetry.
- `afterMCPExecution` → `tool_name`, `tool_input`, `result_json` (full JSON response), `duration` (ms, excludes approval wait).
- Generic `preToolUse`/`postToolUse` also fire for MCP tools (matcher form `MCP:<tool_name>`); `postToolUse` exposes `updated_mcp_tool_output`.

### 3.3 Tool calls
- **Generic:** `preToolUse` (`tool_name`, `tool_input`, `tool_use_id`, `cwd`, `model`), `postToolUse` (+ `tool_output` JSON-stringified payload, `duration`), `postToolUseFailure` (`error_message`, `failure_type` = `error|timeout|permission_denied`, `duration`, `is_interrupt`). Correlate the three via shared `tool_use_id`. Matchers: `Shell`, `Read`, `Write`, `Grep`, `Delete`, `Task`, `MCP:<tool_name>`.
- **Shell:** `beforeShellExecution` (`command`, `cwd`, `sandbox`) / `afterShellExecution` (`command`, full `output`, `duration`, `sandbox`).
- **Files:** `beforeReadFile` (`file_path`, `content`, `attachments[]`) / `afterFileEdit` (`file_path`, `edits[]` with `old_string`/`new_string`).
- **Subagents (Task tool):** `subagentStart` (`subagent_id`, `subagent_type`, `task`, `subagent_model`, `is_parallel_worker`, `tool_call_id`, `parent_conversation_id`) / `subagentStop` (`status`, `summary`, `duration_ms`, `message_count`, `tool_call_count`, `modified_files[]`, `agent_transcript_path`).
- **Session lifecycle:** `sessionStart` (`session_id` == `conversation_id`, `is_background_agent`, `composer_mode`) / `sessionEnd` (`reason`, `duration_ms`, `final_status`, `error_message`).
- **Tab is separate:** inline completions fire `beforeTabFileRead` / `afterTabFileEdit`, not the Agent hooks.

---

## 4. Architecture guidance (important — differs from Claude Code)

1. **Process-per-event, not a resident plugin.** Each hook is a fresh process spawn reading stdin / writing stdout. You cannot hold an open span in memory across events inside the hook. Two viable patterns:
   - (a) Emit each event as a standalone span and **stitch the trace at the collector/backend** using `conversation_id` (root) / `generation_id` (turn) / `tool_use_id` / `subagent_id`.
   - (b) Make the hook a **thin, fast binary** (Go/Rust/Bun) that forwards the event to a **local long-lived daemon** that owns span context. Cursor's own docs demonstrate a `stop` hook POSTing telemetry to an internal endpoint.
   - Keep inline work minimal — `afterFileEdit` and tool hooks fire frequently.

2. **Suggested span tree:**
   ```
   conversation (conversation_id)            [sessionStart → sessionEnd]
     └─ turn (generation_id)                 [beforeSubmitPrompt → afterAgentResponse]
          ├─ tool_call (tool_use_id)         [preToolUse → postToolUse|postToolUseFailure]
          ├─ mcp_call                        [beforeMCPExecution → afterMCPExecution]
          ├─ shell_exec                      [beforeShellExecution → afterShellExecution]
          └─ subagent (subagent_id)          [subagentStart → subagentStop]
   ```

3. **Token/cost gap — inverse of Claude Code.** Cursor hook payloads are rich on *content* but contain **no per-call token counts or cost**. The only token-ish data is in `preCompact` (`context_tokens`, `context_window_size`, `context_usage_percent`). Decide whether our insights require token/cost from Cursor; if so, the only supplement is the aggregate Admin/Analytics API, which won't join at the turn level.

4. **Cloud-agent blind spot.** Cursor Cloud Agents run **command-based hooks only**, and several are **not yet wired** there: `beforeMCPExecution`/`afterMCPExecution`, `afterAgentResponse`/`afterAgentThought`, `beforeSubmitPrompt`, `stop`, and the session hooks. So cloud coverage of prompts/responses/MCP is currently limited to local IDE sessions.

5. **Shared normalization layer.** Aim for a client-agnostic internal event model (`client: 'cursor' | 'claude-code'`, normalized `category`, `tool_name`, `mcp_server`, etc.) with thin per-client adapters. The main per-client divergences are token/cost availability and tracing support.

---

## 5. Suggested next steps in the repo

1. Inspect the current plugin to confirm: how Claude Code events are received, the internal event/normalization model, how spans are opened/closed and correlated, and how metrics are emitted to the collector. **Verify these assumptions against the actual code before refactoring.**
2. Define (or confirm) a client-agnostic internal event schema; add a `cursor` adapter alongside the existing Claude Code one.
3. Implement a thin Cursor hook entrypoint (one small binary/script invoked by all hook events) that serializes the stdin JSON + `hook_event_name` and forwards to the existing pipeline/daemon.
4. Generate a `hooks.json` registering the telemetry-relevant events: `sessionStart`, `sessionEnd`, `beforeSubmitPrompt`, `afterAgentResponse`, `afterAgentThought`, `preToolUse`, `postToolUse`, `postToolUseFailure`, `beforeMCPExecution`, `afterMCPExecution`, `beforeShellExecution`, `afterShellExecution`, `afterFileEdit`, `subagentStart`, `subagentStop`, `preCompact`. Keep all telemetry hooks fail-open.
5. Map fields to OTel attributes — prefer GenAI semantic conventions where they exist (`gen_ai.*`), with a `dash0.*` or vendor namespace for content/metadata not covered by the spec.
6. Add a fixture-based test harness that pipes recorded hook JSON payloads through the adapter and asserts the emitted spans/metrics.

---

## 6. Open decisions

- Do we need token/cost from Cursor at all, given it's absent from hooks?
- Standalone-span-stitching vs local-daemon span context — which fits the existing Claude Code design?
- Content capture policy: prompts, responses, file contents, and MCP results can contain secrets/PII — decide redaction strategy before shipping.
- How to cover Cursor Cloud Agents given the hook gaps.

---

## 7. References

- Cursor Hooks docs: https://cursor.com/docs/hooks.md
- Cursor third-party (Claude Code) hooks: https://cursor.com/docs/reference/third-party-hooks.md
- Cursor Analytics / Admin API (aggregate only): https://cursor.com/docs/account/teams/analytics-api , https://cursor.com/docs/api
- OTel GenAI semantic conventions: https://opentelemetry.io/docs/specs/semconv/gen-ai/
- Claude Code monitoring reference (for parity comparison): https://docs.anthropic.com/en/docs/claude-code/monitoring-usage
