# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

<#
.SYNOPSIS
Dash0 - OpenAI Codex telemetry installer for Windows.

.DESCRIPTION
The Windows counterpart of install-codex.sh, section for section, so the two can
be compared side by side.

Windows PowerShell 5.1 is the target, because that is the version every Windows
install has: no ternary, no null-coalescing, no Join-Path with several child
paths. Everything it needs ships with Windows - curl.exe for downloads and
Get-FileHash for checksums.

.EXAMPLE
irm https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-codex.ps1 | iex

.EXAMPLE
powershell -ExecutionPolicy Bypass -File install-codex.ps1 `
  -Endpoint https://ingress.us1.aws.dash0.com -Token dash0_... -Dataset default

.NOTES
Every flag is optional. A flag that is not supplied is prompted for, or left
blank in a non-interactive run - the plugin then installs but stays inactive
until the config file is filled in.

Env vars: DASH0_OTLP_URL, DASH0_AUTH_TOKEN, DASH0_DATASET, DASH0_TEAM_NAME,
DASH0_VERSION (pins a specific release), DASH0_RAW_REF (pins the git ref the
plugin files come from; for testing before a release).

What this installs:
  %USERPROFILE%\.local\state\dash0-agent-plugin\codex\codex-on-event.ps1
      Bootstrap Codex invokes on each hook event.
  ...\codex\bin\codex-on-event-<v>-windows-<arch>.exe
      The binary the bootstrap runs (pre-downloaded so the connectivity check
      can run before you restart Codex).
  %USERPROFILE%\.codex\dash0-agent-plugin.local.md
      YAML-frontmatter config carrying your OTLP URL + auth token (owner only).
  %USERPROFILE%\.codex\config.toml
      Codex reads hooks from here (there is no hooks.json). This installer
      APPENDS a marker-delimited managed block registering the plugin's hooks
      AND pre-trusting them (Codex requires a persisted trusted_hash or it
      prompts via /hooks). Any hooks you authored yourself are preserved; the
      managed block is replaced on re-install and removed by
      uninstall-codex.ps1.
#>

param(
  [string] $Endpoint = $env:DASH0_OTLP_URL,
  [string] $Token = $env:DASH0_AUTH_TOKEN,
  [string] $Dataset = $env:DASH0_DATASET,
  [string] $Team = $env:DASH0_TEAM_NAME
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$Repo = 'dash0hq/dash0-agent-plugin'

# PowerShell 5.1 decodes a native command's stdout with the console's OEM code
# page, which mangles every non-ASCII byte. The managed block the binary emits
# carries an em dash in its marker comment, so ask for UTF-8 instead. A host
# without a console rejects the assignment, which is harmless here.
try {
  [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
} catch {
  Write-Verbose 'could not switch the console to UTF-8'
}

function Write-Info { param([string] $Message) Write-Host $Message }
function Write-Ok { param([string] $Message) Write-Host "OK  $Message" -ForegroundColor Green }
function Write-Warn { param([string] $Message) Write-Host "!   $Message" -ForegroundColor Yellow }
function Stop-WithError {
  param([string] $Message)
  Write-Host "X   $Message" -ForegroundColor Red
  exit 1
}

# Writes UTF-8 with no byte-order mark. Both parts matter: PowerShell 5.1's
# Set-Content -Encoding utf8 emits a BOM, which agent config parsers reject
# outright, and a CR that survives into a config value corrupts it silently -- an
# auth token with a trailing CR fails to authenticate with nothing reporting why.
#
# -KeepLineEndings turns the CRLF fix off, for the one file this installer does
# not own: rewriting config.toml must not reflow the user's own line endings.
function Write-TextFile {
  param([string] $Path, [string] $Content, [switch] $KeepLineEndings)
  if (-not $KeepLineEndings) { $Content = $Content -replace "`r`n", "`n" }
  $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($Path, $Content, $utf8NoBom)
}

# Restricts a file to its owner, the closest equivalent of chmod 600: drop
# inherited ACEs, then grant only this user and SYSTEM.
function Protect-File {
  param([string] $Path)
  & icacls.exe $Path /inheritance:r /grant:r "${env:USERNAME}:(F)" "SYSTEM:(F)" | Out-Null
  if ($LASTEXITCODE -ne 0) { Write-Warn "could not restrict permissions on $Path" }
}

Write-Host ''
Write-Host 'Dash0 -> OpenAI Codex telemetry installer' -ForegroundColor Cyan
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
# 4. Resolve install paths. The state root matches codex-on-event.ps1 and
#    internal/harness, so the wrapper's binary cache and the binary's own session
#    state stay in one tree.
# ---------------------------------------------------------------------------

if ($env:XDG_STATE_HOME) {
  $StateBase = "$env:XDG_STATE_HOME\dash0-agent-plugin\codex"
} else {
  $StateBase = "$env:USERPROFILE\.local\state\dash0-agent-plugin\codex"
}
$BinDir = "$StateBase\bin"
$BinPath = "$BinDir\codex-on-event-$Version-windows-$Arch.exe"
$ScriptPath = "$StateBase\codex-on-event.ps1"

$CodexDir = "$env:USERPROFILE\.codex"
$ConfigPath = "$CodexDir\dash0-agent-plugin.local.md"
$ConfigToml = "$CodexDir\config.toml"

foreach ($dir in @($BinDir, $CodexDir)) {
  try {
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
  } catch {
    Stop-WithError "could not create $dir"
  }
}

# ---------------------------------------------------------------------------
# 5. Download the binary and verify it. Pre-downloading lets the connectivity
#    check below run before Codex is restarted; the bootstrap would otherwise
#    fetch it on the first hook fire.
# ---------------------------------------------------------------------------

$BaseUrl = "https://github.com/$Repo/releases/download/v$Version"
$BinAsset = "codex-on-event-windows-$Arch.exe"
# Plugin files come from the tagged ref by default. DASH0_RAW_REF overrides it
# with any ref -- a branch, say -- which is the only way to exercise this
# installer before a release carries the PowerShell bootstrap.
$RawRef = $env:DASH0_RAW_REF
if (-not $RawRef) { $RawRef = "v$Version" }
$RawBase = "https://raw.githubusercontent.com/$Repo/$RawRef"

# The binary path is version-pinned, so an already-present binary is exactly this
# version -- skip the download (idempotent re-install; also lets a pre-staged
# binary work offline). A version bump changes $BinPath, forcing a fetch.
if (Test-Path -LiteralPath $BinPath) {
  Write-Ok "binary already present -> $BinPath"
} else {
  Write-Info "downloading codex-on-event v$Version..."
  & curl.exe -fsS -L -o $BinPath "$BaseUrl/$BinAsset"
  if ($LASTEXITCODE -ne 0) {
    Stop-WithError "failed to download the binary: $BaseUrl/$BinAsset (does v$Version publish Windows assets?)"
  }

  $Checksums = & curl.exe -fsS -L "$BaseUrl/checksums.txt"
  if ($LASTEXITCODE -ne 0) { Stop-WithError "failed to download $BaseUrl/checksums.txt" }

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
# 5b. Install the bootstrap script from the tagged ref.
# ---------------------------------------------------------------------------

if (Test-Path -LiteralPath $ScriptPath) {
  Write-Ok "bootstrap already present -> $ScriptPath"
} else {
  Write-Info 'downloading codex-on-event.ps1...'
  # No legacy fallback: the PowerShell bootstrap exists only from the release that
  # introduced Windows support, so pinning DASH0_VERSION to anything older fails
  # here rather than installing something that cannot run.
  & curl.exe -fsS -L -o $ScriptPath "$RawBase/codex/codex-on-event.ps1"
  if ($LASTEXITCODE -ne 0) {
    Stop-WithError "failed to download: $RawBase/codex/codex-on-event.ps1"
  }
  Write-Ok "installed bootstrap -> $ScriptPath"
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

$AgentName = 'codex'
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
# 8. Merge hooks + pre-trust into %USERPROFILE%\.codex\config.toml.
#
#    Codex reads hooks from config.toml and requires a persisted trusted_hash to
#    run them without a /hooks prompt. The binary emits both the [[hooks.*]]
#    blocks and the matching [hooks.state] trust entries, wrapped in markers. We
#    first strip any prior managed block (so re-install is clean and group
#    indices are recomputed against the user's own hooks), then append the fresh
#    block. User-authored hooks outside the markers are never touched.
#
#    The command is written to the plain `command` field, not `commandWindows`:
#    the trust hash covers `command`, and the binary reproduces that hash, so a
#    Windows-only field would leave the hook untrusted. `powershell` resolves
#    from PATH under whatever shell Codex spawns the command with.
#
#    The binary writes the command as a single-quoted TOML literal string, which
#    is what lets a Windows path survive: in a basic string the \U in
#    C:\Users would be read as a unicode escape and the file would not parse.
# ---------------------------------------------------------------------------

Write-Info "registering + pre-trusting hooks in $ConfigToml..."
$HookCommand = "powershell -NoProfile -ExecutionPolicy Bypass -File `"$ScriptPath`""

$BeginMarker = '>>> dash0-agent-plugin (managed)'
$EndMarker = '<<< dash0-agent-plugin (managed)'

# config.toml belongs to the user, so keep the line endings it already uses. A
# file this installer creates gets CRLF, the Windows default.
$Existing = ''
if (Test-Path -LiteralPath $ConfigToml) {
  $Existing = [System.IO.File]::ReadAllText($ConfigToml)
}
$Eol = "`r`n"
if ($Existing -and -not $Existing.Contains("`r`n")) { $Eol = "`n" }

# Drop a prior managed block, line by line, the same way uninstall-codex.ps1 and
# the awk in install-codex.sh do. Untouched when there is nothing to strip.
if ($Existing.Contains($BeginMarker)) {
  $kept = New-Object System.Collections.Generic.List[string]
  $skip = $false
  foreach ($line in (Get-Content -LiteralPath $ConfigToml)) {
    if ($line.Contains($BeginMarker)) { $skip = $true }
    if (-not $skip) { $kept.Add($line) }
    if ($line.Contains($EndMarker)) { $skip = $false }
  }
  $Existing = ''
  if ($kept.Count -gt 0) { $Existing = ($kept -join $Eol) + $Eol }
  # -KeepLineEndings: the rejoin above already decided the endings.
  Write-TextFile $ConfigToml $Existing -KeepLineEndings
}

# PowerShell 5.1 wraps an argument containing spaces in quotes but leaves the
# quotes inside it alone, so Windows' own argument parser eats them -- the binary
# would receive the path unquoted and split on its spaces. Escaping each one as
# \" is what survives the round trip, and the binary gets the exact string it
# hashes and writes.
$Block = & $BinPath emit-codex-hooks --config $ConfigToml --command ($HookCommand -replace '"', '\"')
if ($LASTEXITCODE -ne 0) {
  # The likeliest cause is an apostrophe in the install path: the block writes the
  # command as a TOML literal string, which cannot carry one.
  Stop-WithError 'failed to render hook config'
}

# Separate from any preceding content with a blank line, then append.
$Suffix = ($Block -join $Eol) + $Eol
if ($Existing) { $Suffix = $Eol + $Suffix }
Write-TextFile $ConfigToml ($Existing + $Suffix) -KeepLineEndings
Write-Ok "registered + pre-trusted hooks (managed block in $ConfigToml)"

# ---------------------------------------------------------------------------
# 9. Connectivity check. Feed a fake SessionStart through the binary; it reports
#    the result on stderr.
# ---------------------------------------------------------------------------

if ($Endpoint -and $Token) {
  Write-Info 'running connectivity check...'
  $scratch = Join-Path $env:TEMP ("dash0-install-check-" + [System.Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Force -Path $scratch | Out-Null

  $event = '{"hook_event_name":"SessionStart","session_id":"install-check","model":"gpt-5.5","source":"startup"}'
  $env:DASH0_OTLP_URL = $Endpoint
  $env:CODEX_PLUGIN_OPTION_AUTH_TOKEN = $Token
  $env:DASH0_DATASET = $Dataset
  $env:DASH0_PLUGIN_DATA = $scratch
  try {
    $checkOutput = ($event | & $BinPath 2>&1 | Out-String)
  } finally {
    Remove-Item Env:\DASH0_OTLP_URL, Env:\CODEX_PLUGIN_OPTION_AUTH_TOKEN, `
      Env:\DASH0_DATASET, Env:\DASH0_PLUGIN_DATA -ErrorAction SilentlyContinue
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

# ---------------------------------------------------------------------------
# 10. Done.
# ---------------------------------------------------------------------------

Write-Host ''
Write-Host 'Next steps' -ForegroundColor Cyan
Write-Host "  1. Start a new Codex session (existing sessions won't pick up the new hooks)."
Write-Host '  2. Run a prompt in any repo. Spans should land in your Dash0 dataset with gen_ai.harness.name=codex.'
Write-Host ''
Write-Host 'Hooks are pre-trusted, so Codex should not prompt via /hooks. If it does, run /hooks and trust "dash0".'
Write-Host "To reconfigure later, edit $ConfigPath (no restart needed)."
Write-Host 'To uninstall, run uninstall-codex.ps1.'
