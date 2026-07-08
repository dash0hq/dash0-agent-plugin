# Copilot CLI hook payload capture (Phase 0)

Scaffolding to record real GitHub Copilot CLI hook payloads so we can write the
`internal/source/copilot` adapter and the `events.jsonl` token reader against
ground-truth shapes rather than docs prose.

Everything here is dev-only; captured output lands in `copilot/captured/`, which
is git-ignored.

## What we need to learn (the Phase 0 questions)

1. **Payload format / the D2 question** — does registering **PascalCase** event
   names make the CLI emit the VS-Code-compatible **snake_case** (Claude-shaped)
   payload, while camelCase yields camelCase? This decides whether the normalizer
   is near-identity or Cursor-sized. We register both casings at once (labelled
   `camel` / `pascal`) so a single session captures both shapes side by side.
2. **How the payload is delivered** — docs don't say. The capture script reads
   stdin and also records `argv`; if stdin is empty, inspect argv/env.
3. **`sessionId` presence**, `toolArgs` runtime type (string vs object), and
   which events carry `transcriptPath`.
4. **`events.jsonl`** — confirm `session.assistant_usage` shape, whether it has a
   per-turn boundary marker, and whether usage is flushed by `agentStop` time.
5. **Prompt-mode** — do user-scope command hooks fire under `copilot -p`?

## Setup

1. Install Copilot CLI (already: `copilot --version`) and authenticate (`/login`
   or `COPILOT_GITHUB_TOKEN`).
2. Make the capture script executable:
   ```bash
   chmod +x copilot/capture/capture.sh
   ```
3. Install BOTH capture configs at user scope (they are additive):
   ```bash
   mkdir -p ~/.copilot/hooks
   cp copilot/capture/hooks-camelcase.json  ~/.copilot/hooks/dash0-capture-camel.json
   cp copilot/capture/hooks-pascalcase.json ~/.copilot/hooks/dash0-capture-pascal.json
   ```
   > The `bash` commands use `$HOME/dash0/dash0-agent-plugin/...`. If your checkout
   > lives elsewhere, edit the paths (or set `DASH0_COPILOT_CAPTURE_DIR`). Hooks
   > load at CLI startup, so start a fresh `copilot` after copying.

## Run

Exercise as much as possible in one session (interactive first; then repeat with
`copilot -p "..."` to answer the prompt-mode question):

- a prompt + response,
- a tool call (ask it to run a shell command / read a file),
- a sub-agent / delegated task,
- an error (e.g. a command that fails),
- `/compact` if the session is long enough.

Each hook invocation writes `copilot/captured/<ts>_<label>_<event>.json`.

Then grab the raw session event stream (for the token/model work, D3):

```bash
cp -r ~/.copilot/session-state/<sessionId>/events.jsonl copilot/captured/events.jsonl.sample
```

(Find `<sessionId>` from a captured payload, or `ls -t ~/.copilot/session-state/`.)

## What we'll do with it

- Diff a `camel` vs `pascal` capture of the same logical event → answer D2.
- Confirm field names/renames for the event-mapping table.
- Inspect `events.jsonl.sample` → confirm the `session.assistant_usage` schema,
  turn boundary, and flush timing.
- Store a trimmed, representative subset as fixtures for the unit + simulated-hook
  e2e tests (do NOT commit whole real sessions — scrub any sensitive content).

## Teardown

```bash
rm ~/.copilot/hooks/dash0-capture-camel.json ~/.copilot/hooks/dash0-capture-pascal.json
```
Restart `copilot` to stop capturing. `copilot/captured/` is git-ignored.
