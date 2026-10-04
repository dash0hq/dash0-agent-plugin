---
name: dash0-configure
description: 'Configure the Dash0 telemetry extension for the GitHub Copilot app — write the OTLP URL, auth token, team and dataset to ~/.copilot/dash0-agent-plugin.local.md (or the project-local equivalent). Use when the user wants to set up Dash0, enable telemetry, paste credentials, fix an inactive install, or act on a "dash0: no team configured" message — spans carry no dash0.team.name until the team name is set.'
---

# Configure Dash0 for the Copilot app

The extension reads one config file on every event. Token usage, model, response
and tool details come straight from the app's session events, so there is no
launcher or shell function to install — the CLI plugin's Step B does not apply.

## Trigger

The user wants to configure or reconfigure the Dash0 extension. Two common
shapes:

- **First-time setup.** They have an OTLP URL and an auth token, or the session
  said `dash0: telemetry is not active`.
- **Finishing setup.** Telemetry works, but the session said
  `dash0: no team configured`. Offer it once per session — then leave it alone
  unless the user brings it up.

## Write the config file

Precedence, highest first, so the user isn't surprised when a value doesn't
apply:

1. Project-level config file (`.copilot/dash0-agent-plugin.local.md`)
2. User-level config file (`~/.copilot/dash0-agent-plugin.local.md`)
3. `DASH0_*` environment variables

The Copilot app has no plugin settings UI and no keychain support, and it strips
secret-looking environment variables before they reach an extension, so the
config file is the only dependable place for the token. The Copilot CLI plugin
reads the same files, so one setup covers both.

Ask whether to write user-level (applies to all projects) or project-level (only
the current workspace — takes precedence over the user-level file entirely, does
not merge). Default to user-level unless the user asks for project-only. Below,
`<target>` is the file you settled on. On Windows the user-level file is
`%USERPROFILE%\.copilot\dash0-agent-plugin.local.md`.

> [!WARNING]
> A project-level file takes over the auth token for every session in that
> workspace. If that token is wrong or scoped to a different organization,
> exports fail as a silent 401. Prefer user-level unless the user needs a
> different dataset or team for one project.

1. If `<target>` exists, read it, show current values with `auth_token` masked
   (last 4 chars), and ask before overwriting. If they decline, stop.

2. Decide whether credentials are part of this run. Telemetry is off when the
   session said `dash0: telemetry is not active`; it already works when the
   session said `dash0: connected`, and the user needs only the missing options.

   **When telemetry is off,** ask for both, one at a time. Do not invent a value
   the user did not give.

   - **OTLP URL** (required) — e.g. `https://ingress.us-west-2.aws.dash0.com`
   - **Auth token** (required) — treat as a secret, never echo it back

   **When telemetry already works,** never ask for the token again. Find out
   where it currently comes from, because the answer decides what `<target>`
   must contain. Read `<target>`, and read the other level's file too (the
   user-level one if the target is project-level, and the reverse).

   | Where the credentials are now | What to write |
   |---|---|
   | In `<target>` | Carry its `otlp_url` and `auth_token` lines over verbatim. |
   | In the other level's file, and the target is a different level | Copy that file's `otlp_url` and `auth_token` into `<target>` verbatim. The two files do not merge, so a target without them turns telemetry off on the next session. Tell the user the token will exist in a second file, and stop if they would rather set the option user-level instead. |
   | In neither file | They come from `DASH0_*` environment variables. Write neither key, and tell the user where the values live so they know the file is not the source of truth. If the session still says `dash0: telemetry is not active`, the app stripped them: write both keys into `<target>` instead. |

3. Ask for the recommended values. These are what most installs are missing, so
   ask explicitly rather than defaulting past them.

   - **Team name** (`team_name`) — tags every span with `dash0.team.name`.
     Without it, spans cannot be attributed to a team. Suggest the user's team as
     they would name it in Dash0.
   - **Dataset** (`dataset`) — which Dash0 dataset the data lands in. Leave it
     out and the backend picks its default. There is no literal `default` value
     to write; an empty value means "no dataset header".

4. Offer the remaining options as one batch the user can decline in a single
   answer. Only ask about them if the user wants to change a default.

   | Key | Effect | Default |
   |---|---|---|
   | `agent_name` | Reported as `service.name` | `github-copilot-app` |
   | `omit_io` | Omit prompt content and tool inputs/outputs | `true` |
   | `omit_user_info` | Hash `user.name` and drop `user.email` | `false` |
   | `omit_identity_fallback` | Report only a real `git config user.name`, never the OS account | `false` |
   | `enabled` | Set to `false` to turn the plugin off for this scope without uninstalling | `true` |

   For every key above except `enabled`, `true` and `1` are true and any other
   non-blank value is false, so write `true` or `false` and nothing else.
   `enabled` is the one key the binary reads strictly: only the literal `false`
   turns the plugin off.

5. Show the user the exact file you are about to write, with `auth_token` masked
   to its last 4 chars, and ask them to confirm. Write it only after they agree.
   Omit every key whose value is blank, and include the `otlp_url` and
   `auth_token` lines exactly as step 2 settled them.

   ```
   ---
   otlp_url: "<OTLP_URL>"
   auth_token: "<AUTH_TOKEN>"
   dataset: "<DATASET>"
   team_name: "<TEAM_NAME>"
   # plus any keys the user chose in step 4, in the same key: "value" form
   ---
   ```

6. Restrict `<target>` to its owner, so the token isn't readable by other
   accounts.

   - macOS and Linux: `chmod 600 <target>`
   - Windows: `powershell -NoProfile -Command 'icacls "<target>" /inheritance:r /grant:r "$($env:USERNAME):(F)" "SYSTEM:(F)"'`

   Keep the PowerShell wrapper and the single quotes. A Bash shell rewrites the
   bare `/inheritance:r` and `/grant:r` flags as file paths, and `%USERNAME%`
   expands in `cmd.exe` only.

7. The `dash0: no team configured` warning cannot be silenced. If the user
   deliberately runs without a team, say so plainly rather than looking for a way
   to hide it.

## Finish

> Configuration written. The next turn in this session is exported to your
> Dash0 dataset — the extension re-reads the config on every event, so there is
> nothing to restart. The connectivity check runs only when a session starts,
> so a wrong token is not reported here: start a new session to see
> `dash0: connected`, or check that this session's next turn appears in Dash0.
