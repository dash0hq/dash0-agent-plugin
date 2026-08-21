---
name: dash0-configure
description: 'Configure the Dash0 → GitHub Copilot CLI telemetry integration — write the OTLP URL and auth token to ~/.copilot/dash0-agent-plugin.local.md (or the project-local equivalent) AND install the launch shell function that enables Copilot''s native OpenTelemetry (the per-turn token/cost/model source). Use when the user wants to set up Dash0, enable telemetry, paste credentials, fix an inactive plugin install, or act on a "dash0: no team configured" message — spans carry no dash0.team.name until the team name is set.'
---

# Configure Dash0 for Copilot CLI

Setup has **two parts**:

1. **Config file** — credentials and options the plugin's hook reads on every
   event.
2. **Launch shell function** — shadows `copilot` to enable native OTel into a
   per-session file. The plugin reads that file at each `agentStop` to attach
   per-turn tokens/cost/model to the turn's span. **Without it, spans are still
   emitted but carry no usage data** (Copilot cannot enable native OTel from a
   hook, and it does not pass the file path to hooks — so the launcher owns it,
   using a directory convention the plugin also knows).

## Trigger

The user wants to configure or reconfigure the Dash0 plugin. Two common shapes:

- **First-time setup.** They have an OTLP URL and an auth token, or the session
  said `dash0: telemetry is not active`. Do both steps.
- **Finishing setup.** Telemetry works, but the session said
  `dash0: no team configured`. Step A only, and offer it once per session — then
  leave it alone unless the user brings it up.

## Step A — write the config file

Precedence, highest first, so the user isn't surprised when a value doesn't
apply:

1. Project-level config file (`.copilot/dash0-agent-plugin.local.md`)
2. User-level config file (`~/.copilot/dash0-agent-plugin.local.md`)
3. `DASH0_*` environment variables

Copilot CLI has no plugin settings UI and no keychain support, so the config
file is the only place these values live.

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
   | In neither file | They come from `DASH0_*` environment variables. Write neither key, and tell the user where the values live so they know the file is not the source of truth. |

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
   | `agent_name` | Reported as `service.name` | `github-copilot-cli` |
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

## Step B — install the launch shell function

Pick the snippet for the user's shell. Append it to their profile, replacing any
prior copy between the markers. It enables native OTel into a per-session file
under the convention directory the plugin reads, then runs the real `copilot`. It
sets **no** OTLP endpoint or token — native OTel only writes the local file; the
Dash0 token stays in the config file from Step A.

Write each snippet **verbatim**. Re-quoting it for whatever shell or heredoc does
the appending is how it gets corrupted: doubling the single quotes in the
PowerShell block, for instance, makes the profile unparseable, and the user then
sees spans arrive with no usage data and nothing reporting an error. Ask the user
which shell they launch `copilot` from — on Windows that is PowerShell or cmd, and
they need the matching one.

### bash and zsh (`~/.zshrc`, `~/.bashrc`, …)

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

### PowerShell (the file `$PROFILE` names)

Create the file first if it does not exist:
`New-Item -ItemType File -Force -Path $PROFILE`.

```powershell
# >>> dash0-agent-plugin (copilot) >>>
function copilot {
  # The real CLI, resolved past this function. `command copilot` has no
  # PowerShell equivalent, so the Application lookup is what avoids recursing.
  $exe = Get-Command copilot -CommandType Application -ErrorAction SilentlyContinue |
    Select-Object -First 1
  if (-not $exe) { Write-Error 'copilot is not on PATH'; return }
  $real = $exe.Source

  $dir = "$env:USERPROFILE\.local\state\dash0-agent-plugin\copilot\otel"
  try {
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
  } catch {
    & $real @args   # fail open: no directory, no native OTel, still a working CLI
    return
  }
  $file = Join-Path $dir ("otel-$PID-" + (Get-Random) + ".jsonl")

  $env:COPILOT_OTEL_ENABLED = 'true'
  $env:COPILOT_OTEL_FILE_EXPORTER_PATH = $file
  $env:OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT = 'true'
  try {
    & $real @args
  } finally {
    Remove-Item Env:\COPILOT_OTEL_ENABLED, Env:\COPILOT_OTEL_FILE_EXPORTER_PATH, `
      Env:\OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $file -Force -ErrorAction SilentlyContinue
  }
}
# <<< dash0-agent-plugin (copilot) <<<
```

Two ways the PowerShell version differs from the shell one, both unavoidable:

- **The variables are set on the session, not on one command.** PowerShell has no
  `VAR=value command` form, so the `finally` block removes them again. A session
  that already sets `COPILOT_OTEL_*` for its own reasons loses those values.
- **`$LASTEXITCODE` carries Copilot's exit code out of the function** on its own,
  so there is no `return $rc` to write.

After writing the profile, **verify it parses** — a mangled write (doubled quotes,
say) is a parse error, and a parse error stops the WHOLE profile from loading, so
the function silently never exists and Copilot reports no usage:

```powershell
powershell -Command "if (Get-Command copilot -CommandType Function -ErrorAction SilentlyContinue) { 'ok' } else { 'FUNCTION NOT DEFINED' }"
```

### cmd.exe (a `copilot.cmd` wrapper on PATH)

`cmd.exe` has no profile, so there is nothing to append a function to: its only
per-shell hooks are the `AutoRun` registry value, which fires for every `cmd`
session, and `doskey` macros, which don't work in batch scripts. A wrapper named
`copilot.cmd` in a directory that comes **before** the npm global bin
(`%APPDATA%\npm`) on PATH is the equivalent — `cmd` finds it first, and it calls
the real CLI by the absolute path `where` reports.

Write it to a directory the user already has early on PATH (`%USERPROFILE%\.local\bin`
is a common one), then confirm `where copilot.cmd` lists it first.

```bat
@echo off
rem >>> dash0-agent-plugin (copilot) >>>
setlocal EnableExtensions

rem The real CLI: the first `where` hit that is not this file. Only the
rem cmd-runnable extensions are asked for -- npm also installs an extensionless
rem `copilot` shell script, which `where copilot` returns FIRST and cmd cannot run.
set "DASH0_REAL="
for /f "delims=" %%I in ('where copilot.cmd copilot.exe copilot.bat 2^>nul') do (
  if not defined DASH0_REAL if /i not "%%~fI"=="%~f0" set "DASH0_REAL=%%~fI"
)
if not defined DASH0_REAL (
  echo copilot is not on PATH 1>&2
  exit /b 1
)

set "DASH0_DIR=%USERPROFILE%\.local\state\dash0-agent-plugin\copilot\otel"
if not exist "%DASH0_DIR%" mkdir "%DASH0_DIR%" 2>nul
if not exist "%DASH0_DIR%" (
  rem Fail open: no directory, no native OTel, still a working CLI.
  call "%DASH0_REAL%" %*
  exit /b %ERRORLEVEL%
)

set "DASH0_FILE=%DASH0_DIR%\otel-%RANDOM%-%RANDOM%.jsonl"
set "COPILOT_OTEL_ENABLED=true"
set "COPILOT_OTEL_FILE_EXPORTER_PATH=%DASH0_FILE%"
set "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true"

call "%DASH0_REAL%" %*
set "DASH0_RC=%ERRORLEVEL%"
del "%DASH0_FILE%" 2>nul
rem setlocal scopes the variables, so nothing leaks back to the caller. The rc is
rem expanded before endlocal discards it.
endlocal & exit /b %DASH0_RC%
rem <<< dash0-agent-plugin (copilot) <<<
```

Unlike the other two this one is a file rather than a profile edit, so it takes
effect immediately — no new shell needed. `setlocal` scopes the variables, which
is why it needs no cleanup block.

Notes:
- `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true` makes Copilot write
  prompt/response message content (not just metadata) to the local file — this
  is what lets the plugin surface the **agent response** (`gen_ai.output.messages`);
  without it the file carries usage/model/cost only. The content lives in the
  per-session file (deleted on exit); what actually leaves for Dash0 is still
  gated by the plugin's `omit_io` option (default `true` redacts prompt/response —
  set `omit_io: false` in Step A to export the text).
- The directory `~/.local/state/dash0-agent-plugin/copilot/otel`
  (`%USERPROFILE%\.local\state\dash0-agent-plugin\copilot\otel` on Windows) is a
  fixed convention shared with the plugin — **do not change it** or the plugin
  won't find the file. The `.jsonl` extension is part of it: the reader skips
  every other file in that directory.
- The real CLI is reached past the function by `command copilot` in bash and zsh,
  by the `Get-Command -CommandType Application` lookup in PowerShell, and by the
  `where` hit that is not the wrapper itself in cmd.
- All three are fail-open: if the directory can't be created they fall straight
  through to the real CLI.

## Finish

> Configuration written and the launch function installed. **Open a new shell**
> (or re-source your profile — `. $PROFILE` in PowerShell) and run `copilot` as
> usual — each session now emits canonical spans with per-turn
> token/cost/model to your Dash0 dataset. A `copilot` launched from a shell
> without the function still emits spans, just without usage data.

Re-running Step A takes effect on the next hook fire (the bootstrap re-reads the
config each invocation). Changes to the launch function require a new shell.
