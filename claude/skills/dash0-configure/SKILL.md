---
name: dash0-configure
description: Set up or finish setting up the Dash0 → Claude Code telemetry integration by writing ~/.claude/dash0-agent-plugin.local.md (or the project-local equivalent). Use when the user wants to configure Dash0, enable telemetry, paste credentials, fix an inactive plugin install, or act on a "dash0: no team configured" message — spans carry no dash0.team.name until the team name is set.
---

# Configure Dash0

Write the config file the plugin reads on session start. The file holds every plugin option in YAML frontmatter.

## Trigger

The user wants to configure or reconfigure the Dash0 plugin. Two common shapes:

- **First-time setup.** They have an OTLP URL and an auth token, or the session said `dash0: telemetry is not active`.
- **Finishing setup.** Telemetry works, but the session said `dash0: no team configured`. Offer this once per session, then leave it alone unless the user brings it up.

## Before you start

If the user prefers OS keychain–backed storage for the auth token over a plaintext file, direct them to `/plugin` → **Installed** → **dash0** → **Configure** instead of running this skill, then stop.

Note the precedence order (highest first) so the user isn't surprised when a value doesn't apply:

1. `pluginConfigs` in enterprise-managed settings — users cannot override these
2. `pluginConfigs` in user-level `~/.claude/settings.json`, which is what `/plugin → Configure` writes
3. Project-level config file (`.claude/dash0-agent-plugin.local.md`)
4. User-level config file (`~/.claude/dash0-agent-plugin.local.md`)
5. `DASH0_*` environment variables

If the user already has values set via the UI or managed settings, the file this skill writes is ignored for those keys. Ask them to clear the UI config first, or use the UI directly.

> [!IMPORTANT]
> `auth_token` is the exception to that list. The wrapper loads a config-file token into the same environment variable the UI and managed settings write, so a file token replaces them both, at either level. Never write `auth_token` for a user on a managed rollout without telling them it overrides the org token.

## Scope

Ask whether to write user-level (`~/.claude/dash0-agent-plugin.local.md`, applies to all projects) or project-level (`.claude/dash0-agent-plugin.local.md`, only the current project — overrides the user-level file entirely, does not merge). Default to user-level unless the user asks for project-only.

> [!WARNING]
> Either file sets the auth token in the highest-precedence form, so it takes over the token for every plugin registration the session makes. If that token is wrong or scoped to a different organization, exports fail as a silent 401. A project-level file is the riskier one, because it is the level a user is most likely to point at a one-off token. Prefer user-level unless the user needs a different dataset or team for one project.

## Workflow

1. If the target file already exists, read it and show the user the current values with the `auth_token` masked (show only the last 4 chars). Ask whether to overwrite. If they decline, stop.

2. Decide whether credentials are part of this run. Telemetry is off when the session said `dash0: telemetry is not active`; it already works when the session said `dash0: connected`, and the user needs only the missing options.

   **When telemetry is off,** ask for both of these, one at a time. Do not invent a value the user did not give.

   - **OTLP URL** (required) — Dash0 OTLP ingress, e.g. `https://ingress.us-west-2.aws.dash0.com`
   - **Auth token** (required) — treat as a secret; do not echo it back in later messages

   **When telemetry already works,** never ask the user for the token again. Find out where it currently comes from, because the answer decides what the target file must contain. Read the target file, and read the other level's file too (the user-level one if the target is project-level, and the reverse).

   | Where the credentials are now | What to write |
   |---|---|
   | In the target file | Carry its `otlp_url` and `auth_token` lines over verbatim. |
   | In the other level's file, and the target is a different level | Copy that file's `otlp_url` and `auth_token` into the target verbatim. The two files do not merge, so a target without them turns telemetry off on the next session. Tell the user the token will exist in a second file, and stop if they would rather set the option user-level instead. |
   | In neither file | They come from the plugin UI, managed settings, or the keychain. Write neither key. A file token would replace a working credential with a plaintext copy, and a wrong or differently scoped one fails as a silent 401. |

3. Ask for the recommended values. These are what most installs are missing, so ask explicitly rather than defaulting past them.

   - **Team name** (`team_name`) — tags every span with `dash0.team.name`. Without it, spans cannot be attributed to a team. Suggest the user's team as they would name it in Dash0.
   - **Dataset** (`dataset`) — which Dash0 dataset the data lands in. Leave it out and the backend picks its default. There is no literal `default` value to write; an empty value means "no dataset header".

4. Offer the remaining options as one batch the user can decline in a single answer. Only ask about them if the user wants to change a default.

   | Key | Effect | Default |
   |---|---|---|
   | `agent_name` | Reported as `service.name` | `claude-code` |
   | `omit_io` | Omit prompt content and tool inputs/outputs | `true` |
   | `omit_user_info` | Hash `user.name` and drop `user.email` | `false` |
   | `omit_identity_fallback` | Report only a real `git config user.name`, never the OS account | `false` |
   | `debug` | Print OTel payloads to stderr | `false` |
   | `debug_file` | Append debug output to this path | — |
   | `enabled` | Set to `false` to turn the plugin off for this scope without uninstalling | `true` |
   | `auth_token_keychain_service` | macOS keychain service to read the token from instead of storing it | — |
   | `auth_token_keychain_account` | Optional account for that keychain item | — |

   For every key above except `enabled`, `true` and `1` are true and any other non-blank value is false, so write `true` or `false` and nothing else. `enabled` is parsed by the shell wrapper instead: only the literal `false` turns the plugin off.

5. Show the user the exact file you are about to write, with `auth_token` masked to its last 4 chars, and ask them to confirm. Write it only after they agree. Omit every key whose value is blank, and include the `otlp_url` and `auth_token` lines exactly as step 2 settled them.

   ```
   ---
   otlp_url: "<OTLP_URL>"
   auth_token: "<AUTH_TOKEN>"
   dataset: "<DATASET>"
   team_name: "<TEAM_NAME>"
   # plus any keys the user chose in step 4, in the same key: "value" form
   ---
   ```

6. Run `chmod 600 <file>` so the token isn't world-readable.

7. Tell the user:

   > Configuration written. Run `/reload-plugins` to apply. On next session start you should see `dash0: connected`.

   Re-running this skill later takes effect on the next `/reload-plugins` — no Claude Code restart needed.

   The `dash0: no team configured` warning cannot be silenced. If the user deliberately runs without a team, say so plainly rather than looking for a way to hide it.
