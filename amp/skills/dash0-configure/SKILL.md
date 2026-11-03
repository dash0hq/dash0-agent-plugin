---
name: dash0-configure
description: "Set up or finish setting up the Dash0 → Amp telemetry integration by writing ~/.amp/dash0-agent-plugin.local.md (or the project-local equivalent). Use when the user wants to configure Dash0, enable telemetry, paste credentials, fix a plugin that emits nothing, or find that spans carry no team — they have no dash0.team.name attribute until team_name is set."
---

# Configure Dash0

Write the config file the `amp-on-event` helper reads once per completed turn. The file holds every plugin option in YAML frontmatter.

## Trigger

The user wants to configure or reconfigure the Dash0 plugin. Two common shapes:

- **First-time setup.** They have an OTLP URL and an auth token, or turns are finishing and nothing reaches Dash0.
- **Finishing setup.** Telemetry works, but spans carry no team. Offer this once per session, then leave it alone unless the user brings it up.

## Before you start

Note the precedence order (highest first) so the user isn't surprised when a value doesn't apply:

1. `AMP_PLUGIN_OPTION_<KEY>` environment variables
2. Project-level config file (`.amp/dash0-agent-plugin.local.md`)
3. User-level config file (`~/.amp/dash0-agent-plugin.local.md`)
4. `DASH0_<KEY>` environment variables — ordinary options only, never the auth token

A macOS keychain token configured with `auth_token_keychain_service` wins over the file's `auth_token`. No `.env` file is loaded.

The plugin writes no transcript and keeps no state on disk, so there is nothing else to clean up or inspect when a value looks wrong.

## Scope

Ask whether to write user-level (`~/.amp/dash0-agent-plugin.local.md`, applies to every project on this executor) or project-level (`.amp/dash0-agent-plugin.local.md`, only the current workspace — overrides the user-level file entirely, does not merge). Default to user-level unless the user asks for project-only. On Windows the user-level file is `%USERPROFILE%\.amp\dash0-agent-plugin.local.md`.

> [!WARNING]
> A project-level file takes over the auth token for every session in that workspace. If that token is wrong or scoped to a different organization, exports fail as a silent 401. Prefer user-level unless the user needs a different dataset or team for one project. If you do write a project-level file, check that `.amp/dash0-agent-plugin.local.md` is git-ignored and offer to add it to `.gitignore` if not — it holds the token in cleartext inside a working tree.

An Orb is a fresh executor with none of this on it. Configure Orbs through Amp's environment variables (`/settings/environment-variables`, or `/workspace/environment-variables` for everyone), setting `AMP_PLUGIN_OPTION_OTLP_URL`, `AMP_PLUGIN_OPTION_AUTH_TOKEN`, `AMP_PLUGIN_OPTION_DATASET` and `AMP_PLUGIN_OPTION_TEAM_NAME` — not by writing a file inside one — see `### Orbs` in the plugin's README.

## Team-only recovery

When telemetry already works and only the team name needs changing, use this path instead of the full workflow.

1. Check whether `AMP_PLUGIN_OPTION_TEAM_NAME` overrides the file. If it does, explain that conflict and stop without changing it.
2. Select the active project file if it exists, otherwise the user-level file. Do not create a project override or copy credentials between files.
3. Ask only for the team name. Do not read the whole file into the transcript. Inspect only frontmatter markers and `team_name` lines with a filtered command that prints no credentials.
4. Show the target path and proposed team change and ask for confirmation. Then use a silent line-level read-modify-write command to update the first `team_name` inside the frontmatter or insert it before the closing `---`. Preserve every other line, the Markdown body, and existing permissions. Do not use a file-editing tool that requires a full Read. If no file exists, create an owner-only user-level file with just `team_name` in frontmatter, without credential keys.
5. Tell the user the next completed turn reads the new team name. Stop here.

## Workflow

1. If the target file already exists, inspect its frontmatter through a command that masks `auth_token` before printing anything. Never Read or print the raw file into the transcript. Ask which values to change, not whether to replace the whole file. Preserve every existing key and the Markdown body verbatim unless the user explicitly changes or removes it, including `export_usage`, `enabled`, and `auth_token_keychain_service` / `auth_token_keychain_account`.

2. Decide whether credentials are part of this run.

   **When telemetry is off,** ask for both of these, one at a time. Do not invent a value the user did not give.
   - **OTLP URL** (required) — Dash0 OTLP ingress, e.g. `https://ingress.us-west-2.aws.dash0.com`
   - **Auth token** (required) — treat as a secret; do not echo it back in later messages

   **When telemetry already works,** never ask the user for the token again. Find out where it currently comes from using filtered commands that report key presence and non-secret values only. Keep credentials inside the local read-modify-write process, never in tool output or the transcript.

   | Where the credentials are now                                  | What to write                                                                                                                                                                                                                                                                           |
   | -------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
   | In the target file                                             | Carry its `otlp_url` and `auth_token` lines over verbatim.                                                                                                                                                                                                                              |
   | In the other level's file, and the target is a different level | Copy that file's `otlp_url` and `auth_token` into the target verbatim. The two files do not merge, so a target without them turns telemetry off on the next turn. Tell the user the token will exist in a second file, and stop if they would rather set the option user-level instead. |
   | In neither file                                                | They come from the environment or the keychain. Do not add plaintext credential keys. Preserve any existing keychain service/account settings, and tell the user where the values live.                                                                                                 |

3. Ask for the recommended values. These are what most installs are missing, so ask explicitly rather than defaulting past them.
   - **Team name** (`team_name`) — tags every span with `dash0.team.name`. Without it, spans cannot be attributed to a team. Suggest the user's team as they would name it in Dash0.
   - **Dataset** (`dataset`) — which Dash0 dataset the data lands in. Leave it out and the backend picks its default. There is no literal `default` value to write; an empty value means "no dataset header".

4. Offer the remaining options as one batch the user can decline in a single answer. Only ask about them if the user wants to change a default.

   | Key                      | Effect                                                                                                                                     | Default |
   | ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------ | ------- |
   | `agent_name`             | Reported as `service.name`                                                                                                                 | `amp`   |
   | `omit_io`                | Omit prompt content and tool inputs/outputs                                                                                                | `true`  |
   | `omit_user_info`         | Hash `user.name` and drop `user.email`                                                                                                     | `false` |
   | `omit_identity_fallback` | Report only a real `git config user.name`, never the OS account                                                                            | `false` |
   | `export_usage`           | Read `amp threads export` after each turn for per-model token counts. Amp only. Turning it off keeps the turn's spans and drops the tokens | `true`  |
   | `enabled`                | Set to `false` to turn the plugin off for this scope without uninstalling                                                                  | `true`  |

   For every key above except `enabled`, `true` and `1` are true and any other non-blank value is false, so write `true` or `false` and nothing else. `enabled` is the one key the binary reads strictly: only the literal `false` turns the plugin off.

   `export_usage` is worth naming if the user asks why turns pause or why `amp` authentication matters: it shells out to `amp threads export` in a detached helper after the turn. It never delays the user, but it does need working Amp auth on that machine.

5. Show only the proposed changes, with any `auth_token` value masked, and ask for confirmation. After they agree, edit only the selected keys inside the frontmatter with a silent local read-modify-write command. Keep all untouched keys, credential lines, and the body verbatim. A declined step-4 option means keep its existing value, not remove it. Omit blank keys only when creating a new file or when the user explicitly clears them. Include credential lines exactly as step 2 settled them, without printing them. A new file has this shape:

   ```
   ---
   otlp_url: "<OTLP_URL>"
   auth_token: "<AUTH_TOKEN>"
   dataset: "<DATASET>"
   team_name: "<TEAM_NAME>"
   # plus any keys the user chose in step 4, in the same key: "value" form
   ---
   ```

6. Restrict the file to its owner, so the token isn't readable by other accounts.
   - macOS and Linux: `chmod 600 <file>`
   - Windows: `powershell -NoProfile -Command 'icacls "<file>" /inheritance:r /grant:r "$($env:USERNAME):(F)" "SYSTEM:(F)"'`

   Keep the PowerShell wrapper and the single quotes. A Bash shell rewrites the bare `/inheritance:r` and `/grant:r` flags as file paths, and `%USERNAME%` expands in `cmd.exe` only.

7. Tell the user:

   > Configuration written. The next completed turn exports to your Dash0 dataset — the helper reads this file per turn, so no reload is needed.

   An HTTP success is not proof of storage: the token must also permit span ingestion into the configured dataset. Suggest they check one turn in Dash0 with a read-capable token before relying on it.
