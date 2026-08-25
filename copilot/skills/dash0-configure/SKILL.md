---
name: dash0-configure
description: Configure the Dash0 → GitHub Copilot CLI telemetry integration — write the OTLP URL and auth token to ~/.copilot/dash0-agent-plugin.local.md (or the project-local equivalent) AND install the launch shell function that enables Copilot's native OpenTelemetry (the per-turn token/cost/model source). Use when the user wants to set up Dash0, enable telemetry, paste credentials, fix an inactive plugin install, or act on a "dash0: no team configured" message — spans carry no dash0.team.name until the team name is set.
---

# Configure Dash0 for Copilot CLI

Setup has **two parts**:

1. **Config file** — credentials the plugin's hook reads on every event.
2. **Launch shell function** — shadows `copilot` to enable native OTel into a
   per-session file. The plugin reads that file at each `agentStop` to attach
   per-turn tokens/cost/model to the turn's span. **Without it, spans are still
   emitted but carry no usage data** (Copilot cannot enable native OTel from a
   hook, and it does not pass the file path to hooks — so the launcher owns it,
   using a directory convention the plugin also knows).

## Step A — write the config file

Ask whether to write user-level (`~/.copilot/dash0-agent-plugin.local.md`,
applies to all projects) or project-level
(`.copilot/dash0-agent-plugin.local.md` in the current workspace — takes
precedence over the user-level file entirely, does not merge). Default to
user-level unless the user asks for project-only. Below, `<target>` is the file
you settled on.

1. If `<target>` exists, read it, show current values with `auth_token` masked
   (last 4 chars), and ask before overwriting.

2. Ask for these (one at a time; treat the token as a secret, never echo it):
   - **OTLP URL** (required) — e.g. `https://ingress.us-west-2.aws.dash0.com`
   - **Auth token** (required)
   - **Dataset** (recommended) — blank means the backend picks its default;
     there is no literal `default` value to write
   - **Team name** (recommended) — tags every span with `dash0.team.name`;
     without it spans carry no team attribution

3. Write `<target>` (omit blank optional lines):
   ```
   ---
   otlp_url: "<OTLP_URL>"
   auth_token: "<AUTH_TOKEN>"
   dataset: "<DATASET>"
   team_name: "<TEAM_NAME>"
   ---
   ```
4. `chmod 600 <target>`.

5. The `dash0: no team configured` warning cannot be silenced. If the user
   deliberately runs without a team, say so plainly rather than looking for a way
   to hide it.

## Step B — install the launch shell function

Append this to the user's shell profile (`~/.zshrc`, `~/.bashrc`, …), replacing
any prior copy between the markers. It enables native OTel into a per-session
file under the convention directory the plugin reads, then runs the real
`copilot`. It sets **no** OTLP endpoint or token — native OTel only writes the
local file; the Dash0 token stays in the config file from Step A.

```bash
# >>> dash0-agent-plugin (copilot) >>>
copilot() {
  local d="$HOME/.local/state/dash0-agent-plugin/copilot/otel"
  mkdir -p "$d" 2>/dev/null || { command copilot "$@"; return; }
  local f="$d/otel-$$-${RANDOM:-0}.jsonl"
  COPILOT_OTEL_ENABLED=true COPILOT_OTEL_FILE_EXPORTER_PATH="$f" OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true command copilot "$@"
  local rc=$?
  rm -f "$f"
  return $rc
}
# <<< dash0-agent-plugin (copilot) <<<
```

Notes:
- `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true` makes Copilot write
  prompt/response message content (not just metadata) to the local file — this
  is what lets the plugin surface the **agent response** (`gen_ai.output.messages`);
  without it the file carries usage/model/cost only. The content lives in the
  per-session file (deleted on exit); what actually leaves for Dash0 is still
  gated by the plugin's `omit_io` option (default `true` redacts prompt/response —
  set `omit_io: false` in Step A to export the text).
- The directory `~/.local/state/dash0-agent-plugin/copilot/otel` is a fixed
  convention shared with the plugin — **do not change it** or the plugin won't
  find the file.
- `command copilot` runs the real CLI (avoids recursing into this function).
- It is fail-open: if the directory can't be created it falls straight through
  to `command copilot`.

## Finish

> Configuration written and the launch function installed. **Open a new shell**
> (or `source` your profile) and run `copilot` as usual — each session now
> emits canonical spans with per-turn token/cost/model to your Dash0 dataset.
> A `copilot` launched from a shell without the function still emits spans, just
> without usage data.

Re-running Step A takes effect on the next hook fire (the bootstrap re-reads the
config each invocation). Changes to the launch function require a new shell.
