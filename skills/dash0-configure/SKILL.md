---
name: dash0-configure
description: Configure the Dash0 → Cursor telemetry integration by writing the OTLP URL and auth token to ~/.cursor/dash0-agent-plugin.local.md. Use when the user wants to set up Dash0, enable telemetry, paste credentials, or fix an inactive plugin install.
---

# Configure Dash0

Write the config file the `cursor-on-event` hook reads on every Cursor event. The file holds the OTLP endpoint URL and auth token in YAML frontmatter.

## Trigger

The user wants to configure (or reconfigure) the Dash0 plugin: provide their OTLP URL + auth token, change the dataset, set the agent name, etc.

## Workflow

1. If `~/.cursor/dash0-agent-plugin.local.md` already exists, read it and show the user the current values with the `auth_token` masked (show only the last 4 chars). Ask whether to overwrite. If they decline, stop.

2. Ask the user for these values one at a time. Do not assume any defaults beyond the ones listed.

   - **OTLP URL** (required) — Dash0 OTLP ingress, e.g. `https://ingress.us-west-2.aws.dash0.com`
   - **Auth token** (required) — treat as a secret; do not echo it back in subsequent messages
   - **Dataset** (optional, default `default`)
   - **Agent name** (optional, default `cursor`)
   - **Team name** (optional, blank = unset)

3. Write `~/.cursor/dash0-agent-plugin.local.md` with this exact structure. Omit optional lines whose value is blank.

   ```
   ---
   otlp_url: "<OTLP_URL>"
   auth_token: "<AUTH_TOKEN>"
   dataset: "<DATASET>"
   agent_name: "<AGENT_NAME>"
   team_name: "<TEAM_NAME>"
   ---
   ```

4. Run `chmod 600 ~/.cursor/dash0-agent-plugin.local.md` so the token isn't world-readable.

5. Tell the user:

   > Configuration written. **Quit and relaunch Cursor** (Cmd+Q on macOS) — Cursor only reads `hooks.json` at startup. After that, every prompt you send will emit OTel spans to your Dash0 dataset.

   Re-running this skill later takes effect on the next hook fire without a restart, since the bootstrap script re-reads the config on each invocation.
