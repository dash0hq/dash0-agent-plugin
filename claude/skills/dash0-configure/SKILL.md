---
name: dash0-configure
description: Configure the Dash0 → Claude Code telemetry integration by writing OTLP URL and auth token to ~/.claude/dash0-agent-plugin.local.md (or the project-local equivalent). Use when the user wants to set up Dash0, enable telemetry, paste credentials, or fix an inactive plugin install.
---

# Configure Dash0

Write the config file the plugin reads on session start. The file holds the OTLP endpoint URL and auth token in YAML frontmatter.

## Trigger

The user wants to configure (or reconfigure) the Dash0 plugin: provide their OTLP URL + auth token, change the dataset, set the agent name, etc.

## Before you start

If the user prefers OS keychain–backed storage for the auth token over a plaintext file, direct them to `/plugin` → **Installed** → **dash0** → **Configure** instead of running this skill, then stop.

Note the precedence order (highest first) so the user isn't surprised when a value doesn't apply:

1. `/plugin → Configure` UI
2. Project-level config file (`.claude/dash0-agent-plugin.local.md`)
3. User-level config file (`~/.claude/dash0-agent-plugin.local.md`)
4. `DASH0_*` environment variables

If the user already has values set via the UI, the file this skill writes will be ignored. Ask them to clear the UI config first, or use the UI directly.

## Scope

Ask whether to write user-level (`~/.claude/dash0-agent-plugin.local.md`, applies to all projects) or project-level (`.claude/dash0-agent-plugin.local.md`, only the current project — overrides the user-level file entirely, does not merge). Default to user-level unless the user asks for project-only.

## Workflow

1. If the target file already exists, read it and show the user the current values with the `auth_token` masked (show only the last 4 chars). Ask whether to overwrite. If they decline, stop.

2. Ask the user for these values one at a time. Do not assume any defaults beyond the ones listed.

   - **OTLP URL** (required) — Dash0 OTLP ingress, e.g. `https://ingress.us-west-2.aws.dash0.com`
   - **Auth token** (required) — treat as a secret; do not echo it back in subsequent messages
   - **Dataset** (optional, default `default`)
   - **Agent name** (optional, default `claude-code`)
   - **Team name** (optional, blank = unset)

3. Write the target file with this exact structure. Omit optional lines whose value is blank.

   ```
   ---
   otlp_url: "<OTLP_URL>"
   auth_token: "<AUTH_TOKEN>"
   dataset: "<DATASET>"
   agent_name: "<AGENT_NAME>"
   team_name: "<TEAM_NAME>"
   ---
   ```

4. Run `chmod 600 <file>` so the token isn't world-readable.

5. Tell the user:

   > Configuration written. Run `/reload-plugins` to apply. On next session start you should see `dash0: connected`.

   Re-running this skill later takes effect on the next `/reload-plugins` — no Claude Code restart needed.
