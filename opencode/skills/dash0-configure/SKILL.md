---
name: dash0-configure
description: 'Set up or finish setting up the Dash0 → OpenCode telemetry integration by writing ~/.config/opencode/dash0-agent-plugin.local.md (or the project-local equivalent). Use when the user wants to configure Dash0, enable telemetry, paste credentials, change how much prompt or tool content is sent, fix an inactive plugin install, or act on a "dash0: no team configured" message — spans carry no dash0.team.name until the team name is set.'
---

# Configure Dash0

Write the config file `opencode-on-event.sh` reads on every OpenCode event. The file holds every plugin option in YAML frontmatter.

## Trigger

The user wants to configure or reconfigure the Dash0 plugin. Three common shapes:

- **First-time setup.** They have an OTLP URL and an auth token, or the startup toast said `dash0: telemetry is not active`.
- **Finishing setup.** Telemetry works, but the toast said `dash0: no team configured`. Offer this once per session, then leave it alone unless the user brings it up.
- **Changing what is sent.** They want more or less prompt, tool, skill or sub-agent content in their spans. Jump to step 4 — the credentials are already settled.

## Before you start

Note the precedence order (highest first) so the user isn't surprised when a value doesn't apply:

1. Project-level config file (`.opencode/dash0-agent-plugin.local.md`)
2. User-level config file (`~/.config/opencode/dash0-agent-plugin.local.md`)
3. `DASH0_*` environment variables

OpenCode has no plugin settings UI, so the config file is where these values live. On macOS the auth token can instead be read from the keychain — see step 3.

## Scope

Ask whether to write user-level (applies to all projects) or project-level (only the current project — it overrides the user-level file entirely, it does not merge). Default to user-level unless the user asks for project-only.

> [!WARNING]
> A project-level file takes over the auth token for every session in that project. If that token is wrong or scoped to a different organization, exports fail as a silent 401. Prefer user-level unless the user needs a different dataset or team for one project.

## Workflow

1. If the target file already exists, read it and show the user the current values with the `auth_token` masked (show only the last 4 chars). Ask whether to overwrite. If they decline, stop.

2. Decide whether credentials are part of this run. Telemetry is off when the startup toast said `dash0: telemetry is not active`; it already works when it said `dash0: connected`, and the user needs only the missing options.

   **When telemetry is off,** ask for both of these, one at a time. Do not invent a value the user did not give.

   - **OTLP URL** (required) — Dash0 OTLP ingress, e.g. `https://ingress.us-west-2.aws.dash0.com`
   - **Auth token** (required) — treat as a secret; do not echo it back in later messages

   **When telemetry already works,** never ask the user for the token again. Find out where it currently comes from, because the answer decides what the target file must contain. Read the target file, and read the other level's file too.

   | Where the credentials are now | What to write |
   |---|---|
   | In the target file | Carry its `otlp_url` and `auth_token` lines over verbatim. |
   | In the other level's file, and the target is a different level | Copy that file's `otlp_url` and `auth_token` into the target verbatim. The two files do not merge, so a target without them turns telemetry off on the next session. Tell the user the token will exist in a second file, and stop if they would rather set the option user-level instead. |
   | In neither file | They come from `DASH0_*` environment variables or the keychain. Write neither key, and tell the user where the values live so they know the file is not the source of truth. |

3. On macOS, offer the keychain instead of a plaintext token. Skip this on Linux and Windows, where the wrapper does not read one.

   The user provisions the secret once:

   ```sh
   security add-generic-password -s dash0-agent-plugin -a "$USER" -w
   ```

   and the file then carries only a pointer:

   ```
   auth_token_keychain_service: "dash0-agent-plugin"
   auth_token_keychain_account: "<the account used above>"
   ```

   A successful keychain lookup wins over an inline `auth_token`, and a failed one falls back to it. This does not restrict access by other processes running as the same user; it keeps the secret out of the file.

4. Ask for the recommended values. These are what most installs are missing, so ask explicitly rather than defaulting past them.

   - **Team name** (`team_name`) — tags every span with `dash0.team.name`. Without it, spans cannot be attributed to a team. Suggest the user's team as they would name it in Dash0.
   - **Dataset** (`dataset`) — which Dash0 dataset the data lands in. Leave it out and the backend picks its default. There is no literal `default` value to write; an empty value means "no dataset header".

5. Offer the four privacy dimensions. **OpenCode is the only runtime that has them**, so a user coming from another agent will not expect them. Each is set independently to `disabled`, `limited` or `full`, and each defaults to `limited`.

   | Key | What it governs | At `limited` |
   |---|---|---|
   | `prompts` | prompt and response content, and the session title | the message structure and roles, with each content `<REDACTED>` and its character count reported |
   | `tools` | `execute_tool` spans | the span, its name, timing and status — but not its arguments or result |
   | `skills` | `Skill` calls, which `tools` would otherwise govern | a span naming the skill, without arguments or result |
   | `agents` | `invoke_agent` spans and a sub-agent's own content | the span and the agent name, without content |

   `disabled` drops the whole span for `tools`, `skills` and `agents`, and omits the attributes for `prompts`. `full` sends the content, capped at 16 KB.

   Two things worth saying out loud when the user asks for `full`:

   - A shell call at `limited` still reports its command *shape* — `git status`, `gh repo clone` — never an operand, flag value, path or URL. Most people who reach for `tools: full` actually want that, and already have it.
   - Timing is never privacy. Duration, status and token counts are identical at all three levels.

   If the user has an `omit_io` line already, leave it. It still works and still sets the default for `prompts` and `tools`; an explicit dimension simply outranks it.

6. Offer the remaining options as one batch the user can decline in a single answer. Only ask about them if the user wants to change a default.

   | Key | Effect | Default |
   |---|---|---|
   | `agent_name` | Reported as `service.name` | `opencode` |
   | `omit_io` | Legacy switch: `true` ⇒ `prompts` and `tools` at `limited`, `false` ⇒ both `full` | `true` |
   | `omit_user_info` | Hash `user.name` and drop `user.email` | `false` |
   | `omit_identity_fallback` | Report only a real `git config user.name`, never the OS account | `false` |
   | `enabled` | Set to `false` to turn the plugin off for this scope without uninstalling | `true` |

   For every boolean key above except `enabled`, `true` and `1` are true and any other non-blank value is false, so write `true` or `false` and nothing else. `enabled` is the one key the wrapper reads strictly: only the literal `false` turns the plugin off.

7. Show the user the exact file you are about to write, with `auth_token` masked to its last 4 chars, and ask them to confirm. Write it only after they agree. Omit every key whose value is blank, and include the `otlp_url` and `auth_token` lines exactly as step 2 settled them.

   ```
   ---
   otlp_url: "<OTLP_URL>"
   auth_token: "<AUTH_TOKEN>"
   dataset: "<DATASET>"
   team_name: "<TEAM_NAME>"
   # plus any keys the user chose in steps 3, 5 and 6, in the same key: "value" form
   ---
   ```

   An unrecognized privacy level resolves to `limited` and says so on stderr, so a typo narrows what is exported rather than widening it. That is still a typo — check the spelling before writing.

8. Restrict the file to its owner, so the token isn't readable by other accounts.

   - macOS and Linux: `chmod 600 <file>`
   - Windows: `powershell -NoProfile -Command 'icacls "<file>" /inheritance:r /grant:r "$($env:USERNAME):(F)" "SYSTEM:(F)"'`

   Keep the PowerShell wrapper and the single quotes. A Bash shell rewrites the bare `/inheritance:r` and `/grant:r` flags as file paths, and `%USERNAME%` expands in `cmd.exe` only.

9. Tell the user:

   > Configuration written. The wrapper re-reads this file on every event, so the change takes effect on your next message — no restart needed. **A restart is needed only if you also changed `opencode.json`**, which OpenCode reads once at startup.

   The `dash0: no team configured` warning cannot be silenced. If the user deliberately runs without a team, say so plainly rather than looking for a way to hide it.
