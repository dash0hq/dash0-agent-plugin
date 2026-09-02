# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

<#
.SYNOPSIS
Dash0 - Cursor telemetry installer for Windows.

.DESCRIPTION
The Windows counterpart of install-cursor.sh, section for section, so the two can
be compared side by side.

Windows PowerShell 5.1 is the target, because that is the version every Windows
install has: no ternary, no null-coalescing, no Join-Path with several child
paths. Everything it needs ships with Windows - curl.exe for downloads,
Get-FileHash for checksums, ConvertFrom-Json in place of jq.

.EXAMPLE
irm https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-cursor.ps1 | iex

.EXAMPLE
powershell -ExecutionPolicy Bypass -File install-cursor.ps1 `
  -Endpoint https://ingress.us1.aws.dash0.com -Token dash0_... -Dataset default

.NOTES
Every flag is optional. A flag that is not supplied is prompted for, or left
blank in a non-interactive run - the plugin then installs but stays inactive
until the config file is filled in.

Env vars: DASH0_OTLP_URL, DASH0_AUTH_TOKEN, DASH0_DATASET, DASH0_TEAM_NAME,
DASH0_VERSION (pins a specific release), DASH0_RAW_REF (pins the git ref
the plugin files come from; for testing before a release), DASH0_SOURCE_DIR
(install the plugin files from a local checkout),
DASH0_SKIP_PLUGIN_FILES=1 (leave the plugin files on disk alone; for testing a
locally staged build).
#>

param(
  [string] $Endpoint = $env:DASH0_OTLP_URL,
  [string] $Token = $env:DASH0_AUTH_TOKEN,
  [string] $Dataset = $env:DASH0_DATASET,
  [string] $Team = $env:DASH0_TEAM_NAME
)

Set-StrictMode -Version 2.0
# Saved so it can be put back. The documented `irm ... | iex` install runs this
# text in the caller's session, so 'Stop' would be their setting for as long as
# that window stays open, and their next non-terminating error would abort a
# pipeline that used to survive it. Restored on the way out and in Stop-WithError,
# the two ways this script ends. Set-StrictMode has no such treatment: 5.1 offers
# no way to read the caller's setting back, and -Off could remove a safety net
# they chose deliberately.
$PriorErrorAction = $ErrorActionPreference
$ErrorActionPreference = 'Stop'

$Repo = 'dash0hq/dash0-agent-plugin'

function Write-Info { param([string] $Message) Write-Host $Message }
function Write-Ok { param([string] $Message) Write-Host "OK  $Message" -ForegroundColor Green }
function Write-Warn { param([string] $Message) Write-Host "!   $Message" -ForegroundColor Yellow }
function Restore-CallerSession {
  # Under `irm ... | iex` the caller's session is this script's session, so put
  # back what was changed. $global: is deliberate: a plain assignment inside a
  # function writes a local that dies with the call. Under -File this targets a
  # global that the exiting process discards anyway, so it is a no-op there.
  $global:ErrorActionPreference = $PriorErrorAction
}

# A terminating error anywhere else - an unparseable existing hooks.json, an
# unexpected native stderr - would otherwise unwind past every Restore-CallerSession
# below and leave 'Stop' set in the caller's session. A trap covers the whole
# script without wrapping its body, and `break` re-throws so the run still ends in
# an error rather than limping on.
trap {
  Restore-CallerSession
  break
}

# $PSCommandPath is the script's own path when it runs with -File, and empty when
# the text is executed in a session that already exists - which is what the
# documented `irm ... | iex` install does. `exit` there ends the console process
# itself: verified on Windows PowerShell 5.1, where a failed download closed the
# window before the error could be read. So exit only when there is a process of
# our own to exit, and otherwise raise a terminating error, which unwinds this
# script and leaves the session alone. Both give a scriptblock or -File caller a
# non-zero exit code, which is what a CI run checks.
$RanAsFile = [bool]$PSCommandPath

function Stop-WithError {
  param([string] $Message)
  Write-Host "X   $Message" -ForegroundColor Red
  Restore-CallerSession
  if ($RanAsFile) { exit 1 }
  # The message is deliberately short. PowerShell renders a throw inside an error
  # block that wraps long text mid-URL and repeats it in FullyQualifiedErrorId, so
  # the detail belongs in the line above and this only has to stop the run.
  throw 'install failed'
}

# Writes UTF-8 with no byte-order mark and LF endings. Both matter: PowerShell
# 5.1's Set-Content -Encoding utf8 emits a BOM, which agent JSON parsers reject
# outright, and a CR that survives into a config value corrupts it silently -- an
# auth token with a trailing CR fails to authenticate with nothing reporting why.
function Write-TextFile {
  param([string] $Path, [string] $Content)
  $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($Path, ($Content -replace "`r`n", "`n"), $utf8NoBom)
}

# Restricts a file to its owner, the closest equivalent of chmod 600: drop
# inherited ACEs, then grant only this user and SYSTEM.
function Protect-File {
  param([string] $Path)
  & icacls.exe $Path /inheritance:r /grant:r "${env:USERNAME}:(F)" "SYSTEM:(F)" | Out-Null
  if ($LASTEXITCODE -ne 0) { Write-Warn "could not restrict permissions on $Path" }
}

Write-Host ''
Write-Host 'Dash0 -> Cursor telemetry installer' -ForegroundColor Cyan
Write-Host ''

# ---------------------------------------------------------------------------
# 1. Platform detection.
# ---------------------------------------------------------------------------

# PROCESSOR_ARCHITECTURE reports the *process* architecture, so a 32-bit host
# process on 64-bit Windows says x86 and puts the machine's real architecture in
# PROCESSOR_ARCHITEW6432.
$Machine = $env:PROCESSOR_ARCHITEW6432
if (-not $Machine) { $Machine = $env:PROCESSOR_ARCHITECTURE }
if ($Machine -eq 'ARM64') {
  $Arch = 'arm64'
} elseif ($Machine -eq 'AMD64') {
  $Arch = 'amd64'
} else {
  Stop-WithError "unsupported architecture: $Machine (need amd64 or arm64)"
}
Write-Ok "detected windows/$Arch"

# ---------------------------------------------------------------------------
# 2. Check the tools this needs. All ship with Windows, so a failure here means
#    something unusual rather than a missing install step.
# ---------------------------------------------------------------------------

if (-not (Get-Command curl.exe -ErrorAction SilentlyContinue)) {
  Stop-WithError 'curl.exe not found (ships with Windows 10 1803 and later)'
}

# ---------------------------------------------------------------------------
# 3. Resolve the version. DASH0_VERSION pins a release; otherwise ask GitHub for
#    the latest published tag.
# ---------------------------------------------------------------------------

$Version = $env:DASH0_VERSION
if (-not $Version) {
  Write-Info 'resolving latest release...'
  $Latest = & curl.exe -fsS -L "https://api.github.com/repos/$Repo/releases/latest"
  if ($LASTEXITCODE -ne 0) {
    Stop-WithError 'could not reach the GitHub API; set DASH0_VERSION to pin a specific version'
  }
  $Match = [regex]::Match(($Latest -join "`n"), '"tag_name"\s*:\s*"v?([^"]+)"')
  if (-not $Match.Success) {
    Stop-WithError 'could not resolve the latest release; set DASH0_VERSION to pin a specific version'
  }
  $Version = $Match.Groups[1].Value
}
Write-Ok "using v$Version"

# ---------------------------------------------------------------------------
# 4. Resolve install paths. The state root matches cursor-on-event.ps1 and
#    internal/harness, so the wrapper's binary cache and the binary's own session
#    state stay in one tree.
# ---------------------------------------------------------------------------

if ($env:XDG_STATE_HOME) {
  $StateBase = "$env:XDG_STATE_HOME\dash0-agent-plugin\cursor"
} else {
  $StateBase = "$env:USERPROFILE\.local\state\dash0-agent-plugin\cursor"
}
$BinDir = "$StateBase\bin"
$BinPath = "$BinDir\cursor-on-event-$Version-windows-$Arch.exe"

$PluginDir = "$env:USERPROFILE\.cursor\plugins\local\dash0-agent-plugin"
$ScriptPath = "$PluginDir\cursor\cursor-on-event.ps1"
$ManifestPath = "$PluginDir\.cursor-plugin\plugin.json"
$HooksManifestPath = "$PluginDir\cursor\hooks.json"
$SkillsDir = "$PluginDir\cursor\skills"

$ConfigPath = "$env:USERPROFILE\.cursor\dash0-agent-plugin.local.md"
$HooksPath = "$env:USERPROFILE\.cursor\hooks.json"

foreach ($dir in @($BinDir, "$PluginDir\.cursor-plugin", "$PluginDir\cursor", $SkillsDir, "$env:USERPROFILE\.cursor")) {
  try {
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
  } catch {
    Stop-WithError "could not create $dir"
  }
}

# ---------------------------------------------------------------------------
# 5. Download the binary and verify it. Pre-downloading lets the connectivity
#    check below run before Cursor is relaunched; the bootstrap would otherwise
#    fetch it on the first hook fire.
# ---------------------------------------------------------------------------

$BaseUrl = "https://github.com/$Repo/releases/download/v$Version"
$BinAsset = "cursor-on-event-windows-$Arch.exe"
# Plugin files come from the tagged ref by default. DASH0_RAW_REF overrides it
# with any ref -- a branch, say -- which is the only way to exercise this
# installer before a release carries the PowerShell bootstrap.
$RawRef = $env:DASH0_RAW_REF
if (-not $RawRef) { $RawRef = "v$Version" }
$RawBase = "https://raw.githubusercontent.com/$Repo/$RawRef"

# DASH0_SOURCE_DIR installs the plugin files from a local checkout instead of the
# ref above. With a pre-staged binary that makes the whole install offline, and it
# is what lets the install contract test THIS checkout's bootstrap rather than the
# last release's. Mirrors install-cursor.sh.
$SourceDir = $env:DASH0_SOURCE_DIR
if ($SourceDir) {
  if (-not (Test-Path -LiteralPath $SourceDir -PathType Container)) {
    Stop-WithError "DASH0_SOURCE_DIR is not a directory: $SourceDir"
  }
  Write-Info "installing plugin files from $SourceDir"
}

# Set DASH0_SKIP_PLUGIN_FILES=1 to leave the plugin files on disk untouched. That
# is how a locally built plugin is tested before a release carries the Windows
# files; nothing else should set it. Without it every run fetches and replaces, so
# re-running the installer really upgrades - which is the point of the change: the
# old skip-if-present made an upgrade a no-op, leaving a bootstrap with the
# previous $Version baked into it while the hook went on resolving the previous
# binary.
$KeepStagedPluginFiles = $env:DASH0_SKIP_PLUGIN_FILES -eq '1'

# The binary path is version-pinned, so an already-present binary is exactly this
# version -- skip the download (idempotent re-install; also lets a pre-staged
# binary work offline, which is how a local build is tested before a release
# carries Windows assets). A version bump changes $BinPath, forcing a fetch.
if (Test-Path -LiteralPath $BinPath) {
  Write-Ok "binary already present -> $BinPath"
} else {
  Write-Info "downloading cursor-on-event v$Version..."
  # Every failure below removes $BinPath before stopping. curl creates the file
  # before it learns the request failed, so a 404 leaves an empty one behind, and
  # the skip above would then report "binary already present" on the next run and
  # never verify anything.
  & curl.exe -fsS -L -o $BinPath "$BaseUrl/$BinAsset"
  if ($LASTEXITCODE -ne 0) {
    Remove-Item -LiteralPath $BinPath -Force -ErrorAction SilentlyContinue
    Stop-WithError "failed to download the binary: $BaseUrl/$BinAsset (does v$Version publish Windows assets?)"
  }

  $Checksums = & curl.exe -fsS -L "$BaseUrl/checksums.txt"
  if ($LASTEXITCODE -ne 0) {
    Remove-Item -LiteralPath $BinPath -Force -ErrorAction SilentlyContinue
    Stop-WithError "failed to download $BaseUrl/checksums.txt"
  }

  # Fail closed, like the shell installer and the bootstraps: a binary that cannot
  # be verified is deleted rather than installed.
  $Expected = ''
  foreach ($Line in $Checksums) {
    $Fields = -split $Line
    if ($Fields.Length -eq 2 -and $Fields[1] -eq $BinAsset) { $Expected = $Fields[0] }
  }
  if (-not $Expected) {
    Remove-Item -LiteralPath $BinPath -Force -ErrorAction SilentlyContinue
    Stop-WithError "no checksum for $BinAsset in v$Version - refusing to install an unverified binary"
  }
  $Actual = (Get-FileHash -LiteralPath $BinPath -Algorithm SHA256).Hash
  # -ne on strings is case-insensitive, which pairs Get-FileHash's upper-case digest
  # with the lower-case one in checksums.txt.
  if ($Actual -ne $Expected) {
    Remove-Item -LiteralPath $BinPath -Force -ErrorAction SilentlyContinue
    Stop-WithError "checksum mismatch for $BinAsset (expected $Expected, got $Actual)"
  }
  Write-Ok "installed binary -> $BinPath"
}

# ---------------------------------------------------------------------------
# 5b. Install the plugin files, fetched from the tagged ref so the on-disk layout
#     matches what a native Cursor marketplace install would produce.
# ---------------------------------------------------------------------------

# Every run fetches and replaces, matching install-cursor.sh, so re-running the
# installer really upgrades. DASH0_SKIP_PLUGIN_FILES is the one way to keep what is
# on disk; a failed download is not, because an upgrade that quietly kept the old
# files would report success while the hook went on running the old code.
function Install-PluginFile {
  param([string] $Source, [string] $Destination)
  if ($KeepStagedPluginFiles) {
    if (-not (Test-Path -LiteralPath $Destination)) {
      Stop-WithError "DASH0_SKIP_PLUGIN_FILES is set but $Destination is not there"
    }
    Write-Ok "kept staged file -> $Destination"
    return
  }
  if ($SourceDir) {
    # $Source is repo-relative with forward slashes; make it a native path so the
    # -LiteralPath below is not asked to interpret a mixed separator.
    $Local = Join-Path $SourceDir ($Source -replace '/', '\')
    if (-not (Test-Path -LiteralPath $Local -PathType Leaf)) {
      Stop-WithError "not found in ${SourceDir}: $Source"
    }
    Write-Info "copying $Source from $SourceDir..."
    try {
      Copy-Item -LiteralPath $Local -Destination $Destination -Force
    } catch {
      # The same locked destination the download branch below tolerates, and
      # Copy-Item throws on it under $ErrorActionPreference = 'Stop'.
      Write-Warn "could not replace $Destination (a running hook may hold it open) - quit Cursor and re-run to refresh it"
      return
    }
    Write-Ok "installed -> $Destination"
    return
  }
  Write-Info "downloading $Source..."
  # Staged, not written straight to $Destination: curl creates the file before it
  # learns the request failed, so a 404 would truncate a copy that works.
  $tmp = "$Destination.tmp.$PID"
  & curl.exe -fsS -L -o $tmp "$RawBase/$Source"
  if ($LASTEXITCODE -ne 0) {
    Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
    Stop-WithError "failed to download: $RawBase/$Source"
  }
  try {
    Move-Item -LiteralPath $tmp -Destination $Destination -Force
  } catch {
    Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
    # Windows will not replace a file another process holds open, and a hook firing
    # right now holds this one. What is on disk still runs, so name the one action
    # that fixes it rather than abandon a half-done install.
    Write-Warn "could not replace $Destination (a running hook may hold it open) - quit Cursor and re-run to refresh it"
    return
  }
  Write-Ok "installed -> $Destination"
}

Install-PluginFile '.cursor-plugin/plugin.json' $ManifestPath
Install-PluginFile 'cursor/hooks.json' $HooksManifestPath
# No legacy fallback: the PowerShell bootstrap exists only from the release that
# introduced Windows support, so pinning DASH0_VERSION to anything older fails
# here rather than installing something that cannot run.
Install-PluginFile 'cursor/cursor-on-event.ps1' $ScriptPath

foreach ($skill in @('dash0-configure')) {
  $skillDir = "$SkillsDir\$skill"
  New-Item -ItemType Directory -Force -Path $skillDir | Out-Null
  Install-PluginFile "cursor/skills/$skill/SKILL.md" "$skillDir\SKILL.md"
}

# ---------------------------------------------------------------------------
# 6. Collect configuration. Precedence: parameter or env var, then prompt, then
#    skip with a warning.
# ---------------------------------------------------------------------------

function Read-Value {
  param([string] $Current, [string] $Label, [string] $Default = '')
  if ($Current) { return $Current }
  if (-not [Environment]::UserInteractive) { return $Default }
  if ($Default) {
    $answer = Read-Host "$Label [$Default]"
  } else {
    $answer = Read-Host $Label
  }
  if (-not $answer) { return $Default }
  return $answer
}

function Read-Secret {
  param([string] $Current, [string] $Label)
  if ($Current) { return $Current }
  if (-not [Environment]::UserInteractive) { return '' }
  $secure = Read-Host "$Label (input hidden)" -AsSecureString
  $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
  try {
    return [Runtime.InteropServices.Marshal]::PtrToStringAuto($bstr)
  } finally {
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
  }
}

$AgentName = 'cursor'
$Endpoint = Read-Value $Endpoint 'Dash0 OTLP endpoint URL (e.g. https://ingress.<region>.aws.dash0.com)'
$Token = Read-Secret $Token 'Dash0 auth token'
$Dataset = Read-Value $Dataset 'Dash0 dataset (optional)' 'default'
$Team = Read-Value $Team 'Team name (optional)'

if (-not $Endpoint -or -not $Token) {
  Write-Warn 'OTLP URL or auth token not provided. The plugin installs but stays inactive.'
  Write-Warn "Re-run with -Endpoint and -Token, or edit $ConfigPath later."
}

# ---------------------------------------------------------------------------
# 7. Write the config file, restricted to this user: it holds the token in
#    cleartext.
# ---------------------------------------------------------------------------

$lines = @('---', "otlp_url: `"$Endpoint`"", "auth_token: `"$Token`"")
if ($Dataset) { $lines += "dataset: `"$Dataset`"" }
if ($AgentName) { $lines += "agent_name: `"$AgentName`"" }
if ($Team) { $lines += "team_name: `"$Team`"" }
$lines += '---'
Write-TextFile $ConfigPath (($lines -join "`n") + "`n")
Protect-File $ConfigPath
Write-Ok "wrote config -> $ConfigPath (owner only)"

# ---------------------------------------------------------------------------
# 8. Merge the hook registrations into ~/.cursor/hooks.json.
#
#    Cursor does not fire hooks declared in a local plugin manifest, only those
#    in this file (user scope) or a project's .cursor/hooks.json. The command is
#    written as a fully resolved absolute path: the shell installer relies on
#    Cursor expanding a literal $HOME at hook time, which is not a promise worth
#    depending on here.
#
#    Merge strategy matches the shell installer: keep every entry whose command
#    does not mention a cursor-on-event bootstrap, drop the ones that do -- which
#    covers both a previous install and the legacy paths -- and add the fresh set.
# ---------------------------------------------------------------------------

Write-Info "merging Dash0 hook registrations into $HooksPath..."

$HookCommand = "& `"$ScriptPath`""
# -Encoding UTF8: without it, Get-Content reads a BOM-less JSON file under the
# system's legacy codepage, corrupting any non-ASCII byte before ConvertFrom-Json
# ever sees it.
$manifest = Get-Content -LiteralPath $HooksManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json

$merged = [ordered]@{}
if (Test-Path -LiteralPath $HooksPath) {
  $existing = Get-Content -LiteralPath $HooksPath -Raw -Encoding UTF8 | ConvertFrom-Json
  if ($existing.PSObject.Properties['hooks']) {
    foreach ($property in $existing.hooks.PSObject.Properties) {
      $kept = @()
      foreach ($entry in @($property.Value)) {
        if ($entry.PSObject.Properties['command'] -and "$($entry.command)" -like '*cursor-on-event*') { continue }
        $kept += $entry
      }
      $merged[$property.Name] = $kept
    }
  }
}

foreach ($property in $manifest.hooks.PSObject.Properties) {
  $ours = @()
  foreach ($entry in @($property.Value)) {
    # Mirrors the shell installer's `map(.command = $cmd)`: keep whatever else the
    # manifest entry declares, override only the command.
    $copy = [ordered]@{}
    foreach ($field in $entry.PSObject.Properties) { $copy[$field.Name] = $field.Value }
    $copy['command'] = $HookCommand
    $ours += [pscustomobject]$copy
  }
  if ($merged.Contains($property.Name)) {
    $merged[$property.Name] = @($merged[$property.Name]) + $ours
  } else {
    $merged[$property.Name] = $ours
  }
}

# Cursor reads each event's value as a list, so every one has to serialize as a
# JSON array even when it holds a single entry. Two things make that so: each
# value is cast to [object[]], and the object goes to ConvertTo-Json through
# -InputObject rather than the pipeline, which unwraps a single-element array
# before the serializer ever sees it.
foreach ($name in @($merged.Keys)) {
  $merged[$name] = [object[]]@($merged[$name])
}
$output = [pscustomobject]@{ version = 1; hooks = [pscustomobject]$merged }
Write-TextFile $HooksPath ((ConvertTo-Json -InputObject $output -Depth 10) + "`n")
Write-Ok "registered hooks -> $HooksPath"

# ---------------------------------------------------------------------------
# 9. Connectivity check. Feed a fake sessionStart through the binary; it reports
#    the result on stderr.
# ---------------------------------------------------------------------------

if ($Endpoint -and $Token) {
  Write-Info 'running connectivity check...'
  $scratch = Join-Path $env:TEMP ("dash0-install-check-" + [System.Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Force -Path $scratch | Out-Null

  $event = '{"hook_event_name":"sessionStart","session_id":"install-check","conversation_id":"install-check","model":"default"}'
  # No credentials are passed in. The binary resolves otlp_url, auth_token and
  # dataset from the config file written in step 7, exactly as it will on a real
  # hook fire, so this validates that file rather than the values held in this
  # script. Passing the token as CURSOR_PLUGIN_OPTION_AUTH_TOKEN would outrank the
  # file and hide a token the installer wrote but the binary cannot use.
  $env:DASH0_PLUGIN_DATA = $scratch
  # The script sets $ErrorActionPreference = 'Stop' globally, which in PS 5.1 turns
  # a native command's stderr output into a terminating error. The binary reports
  # its connectivity result on stderr by design, so this call needs 'Continue'
  # locally to actually capture that text instead of aborting on it.
  $PriorErrorActionPreference = $ErrorActionPreference
  $ErrorActionPreference = 'Continue'
  # Run from the scratch directory. A project-level config file in the current
  # directory outranks the user-level one, and this check has no business
  # validating some unrelated repository's configuration.
  Push-Location -LiteralPath $scratch
  try {
    $checkOutput = ($event | & $BinPath 2>&1 | Out-String)
  } finally {
    Pop-Location
    $ErrorActionPreference = $PriorErrorActionPreference
    Remove-Item Env:\DASH0_PLUGIN_DATA -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $scratch -Recurse -Force -ErrorAction SilentlyContinue
  }

  if ($checkOutput -like '*connectivity check failed*') {
    Write-Warn 'connectivity check failed:'
    Write-Host "    $checkOutput"
  } elseif ($checkOutput -like '*connected*') {
    Write-Ok 'connectivity check passed'
  } else {
    Write-Warn 'connectivity check returned unexpected output:'
    Write-Host "    $checkOutput"
  }
}

# The token lives in the config file from here on. Under the documented
# `irm ... | iex` install the script's variables outlive the run, because the text
# executes in the caller's session, so both variables that carry the token are
# dropped rather than left reachable through Get-Variable for as long as the window
# stays open. A failure between the config write and this point still leaves them
# set; the file, not the session, is where the token is meant to be.
Remove-Variable Token, lines -ErrorAction SilentlyContinue
Restore-CallerSession

# ---------------------------------------------------------------------------
# 10. Done.
# ---------------------------------------------------------------------------

Write-Host ''
Write-Host 'Next steps' -ForegroundColor Cyan
Write-Host "  1. Quit Cursor and relaunch it - Cursor scans $env:USERPROFILE\.cursor\plugins\local\ on startup."
Write-Host '  2. Open any repo in Cursor and run a prompt. Spans should land in your Dash0 dataset.'
Write-Host ''
Write-Host "To reconfigure later, edit $ConfigPath (no restart needed)."
Write-Host 'To uninstall, run uninstall-cursor.ps1.'
