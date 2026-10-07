---
name: dash0-configure
description: 'Configure the Dash0 → OpenCode V2 telemetry integration: write the OTLP URL, auth token and options to ~/.opencode-v2/dash0-agent-plugin.local.md (or the project-local equivalent), and keep the token out of opencode.json. Use when the user wants to set up Dash0, enable telemetry, paste credentials, change the team or dataset, or move a token out of opencode.json.'
---

# Configure Dash0 for OpenCode V2

The plugin's exporter reads its settings from a config file, so the token never
has to live in `opencode.json`, which is often committed with a project.

Precedence, highest first, so the user isn't surprised when a value doesn't
apply:

1. `options` on the plugin's entry in `opencode.json`
2. Project-level config file (`.opencode-v2/dash0-agent-plugin.local.md` in the
   project directory)
3. User-level config file (`~/.opencode-v2/dash0-agent-plugin.local.md`;
   `%USERPROFILE%\.opencode-v2\dash0-agent-plugin.local.md` on Windows)
4. `DASH0_*` environment variables (never used for the token)

The two files do not merge: a project file replaces the user file entirely.

## Step 1: pick the file

Ask whether to write user-level (all projects) or project-level (only this
project). Default to user-level. Below, `<target>` is the file you settled on.

> [!WARNING]
> A project-level file takes over the auth token for every session in that
> project. A wrong token there fails exports as a silent 401. Prefer user-level
> unless the user needs a different dataset or team for one project. If you do
> write a project-level file, check that `.opencode-v2/` is git-ignored and offer
> to add it to `.gitignore` if not.

## Step 2: collect values

1. If `<target>` exists, read it, show the current values with `auth_token`
   masked (last 4 chars), and ask before overwriting. If they decline, stop.
2. Ask for these, one at a time. Do not invent a value the user did not give.
   If `<target>` already has working credentials and the user only wants to
   change an option, carry `otlp_url` and `auth_token` over verbatim instead.
   - **OTLP URL** (required), e.g. `https://ingress.us-west-2.aws.dash0.com`
   - **Auth token** (required). Treat it as a secret and never echo it back.
   - **Team name** (`team_name`, recommended): tags every span with
     `dash0.team.name`.
   - **Dataset** (`dataset`): leave it out for the backend's default.
3. Offer the remaining options as one batch the user can decline in one answer.

   | Key | Effect | Default |
   |---|---|---|
   | `agent_name` | Reported as `service.name` | `opencode-v2` |
   | `omit_io` | Omit prompt content and tool inputs/outputs | `true` |
   | `omit_user_info` | Hash `user.name` and drop `user.email` | `false` |
   | `omit_identity_fallback` | Report only a real `git config user.name`, never the OS account | `false` |
   | `enabled` | `false` turns the plugin off for this scope without uninstalling; `false` in the file wins even over `opencode.json` | `true` |

   Write `true` or `false` for these, nothing else.

On macOS the token can stay out of every file: store it with
`security add-generic-password -s dash0-opencode -a "$USER" -w` (it prompts for
the token) and write `auth_token_keychain_service: "dash0-opencode"` instead of
`auth_token`. A keychain token takes precedence over every other source.

## Step 3: write the file

1. Show the exact file with `auth_token` masked and ask the user to confirm.
   Omit every key whose value is blank.

   ```
   ---
   otlp_url: "<OTLP_URL>"
   auth_token: "<AUTH_TOKEN>"
   dataset: "<DATASET>"
   team_name: "<TEAM_NAME>"
   # plus any keys chosen in step 2.3, in the same key: "value" form
   ---
   ```

2. Write it, creating the parent directory if needed, then restrict it to its
   owner:
   - macOS and Linux: `chmod 600 <target>`
   - Windows: `powershell -NoProfile -Command 'icacls "<target>" /inheritance:r /grant:r "$($env:USERNAME):(F)" "SYSTEM:(F)"'`

   Do not use `chmod` on Windows, even from a Bash shell. It sets the read-only
   bit and leaves the NTFS permissions untouched, so the token stays readable by
   every account on the machine.

   Keep the PowerShell wrapper and the single quotes. A Bash shell rewrites the
   bare `/inheritance:r` and `/grant:r` flags as file paths, and `%USERNAME%`
   expands in `cmd.exe` only.

## Step 4: clean up opencode.json

Options on the plugin's `opencode.json` entry outrank the file. Read the global
config (`~/.config/opencode/opencode.json`) and the project's `opencode.json`,
find the entry whose `package` is this plugin, and list any of the keys above
set in its `options`.

- If `auth_token` is there, tell the user it overrides the file and sits in plain
  text, and offer to remove it. Removing it is the point of this skill.
- For any other key that conflicts with what was just written, say which value
  wins and offer to remove it from `options`.

Change `opencode.json` only with the user's agreement, and leave the rest of the
file untouched.

## Finish

The exporter reads its settings when it starts, so tell the user to run
`opencode reload` (with `--server <url>` for their own `opencode serve`). After that, every session is exported to their Dash0 dataset.
