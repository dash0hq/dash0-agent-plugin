# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

<#
.SYNOPSIS
Dash0 - Amp CLI and Orb telemetry installer for Windows.

.DESCRIPTION
The Windows counterpart of install-amp.sh, section for section, so the two can
be compared side by side.

Windows PowerShell 5.1 is the target, because that is the version every Windows
install has: no ternary, no null-coalescing, no Join-Path with several child
paths. Everything it needs ships with Windows - curl.exe for downloads and
System.Security.Cryptography for checksums.

.EXAMPLE
irm https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/install-amp.ps1 | iex

.EXAMPLE
powershell -ExecutionPolicy Bypass -File install-amp.ps1 `
  -Endpoint https://ingress.us1.aws.dash0.com -Token dash0_... -Dataset default

.NOTES
Every flag is optional. Missing values are prompted for, with existing values
as defaults. Enter keeps the current value; - clears an optional dataset/team.
Without a terminal, existing values are kept. A new install without credentials
stays inactive until the config file is filled in.

-Project installs into .\.amp\plugins\dash0 instead of the user directory.

Env vars: DASH0_OTLP_URL, DASH0_AUTH_TOKEN, DASH0_DATASET, DASH0_TEAM_NAME,
DASH0_VERSION (pins a specific release), DASH0_SOURCE_DIR (install index.ts and
amp-on-event.exe from a local checkout instead of a release, for developing this
plugin or installing without network:
  go build -o amp\amp-on-event.exe .\cmd\amp-on-event
  $env:DASH0_SOURCE_DIR = 'amp'; .\install-amp.ps1).

What this installs:
  %USERPROFILE%\.config\amp\plugins\dash0\index.ts
      The plugin Amp loads (Bun runs it).
  %USERPROFILE%\.config\amp\plugins\dash0\amp-on-event.exe
      The helper index.ts spawns. Amp's bridge resolves it by that exact name
      next to index.ts, so the release asset amp-on-event-windows-<arch>.exe is
      installed under the unsuffixed name.
  %USERPROFILE%\.amp\dash0-agent-plugin.local.md
      YAML-frontmatter config carrying your OTLP URL + auth token (owner only).

Amp loads a directory plugin: there is no hook and no settings file to register,
so unlike install-codex.ps1 this writes nothing outside the plugin directory and
the config file. There is no connectivity check either - amp-on-event only
accepts a completed turn, not a probe event.
#>

param(
  [string] $Endpoint = $env:DASH0_OTLP_URL,
  [string] $Token = $env:DASH0_AUTH_TOKEN,
  [string] $Dataset = $env:DASH0_DATASET,
  [string] $Team = $env:DASH0_TEAM_NAME,
  [switch] $Project
)

Set-StrictMode -Version 2.0
# Saved so it can be put back. The documented `irm ... | iex` install runs this
# text in the caller's session, so 'Stop' would be their setting for as long as
# that window stays open, and their next non-terminating error would abort a
# pipeline that used to survive it. Restored on the way out and in Stop-WithError,
# the two ways this script ends.
$PriorErrorAction = $ErrorActionPreference
$ErrorActionPreference = 'Stop'

$Repo = 'dash0hq/dash0-agent-plugin'

function Write-Info { param([string] $Message) Write-Host $Message }
function Write-Ok { param([string] $Message) Write-Host "OK  $Message" -ForegroundColor Green }
function Write-Warn { param([string] $Message) Write-Host "!   $Message" -ForegroundColor Yellow }
function Restore-CallerSession {
  # Under `irm ... | iex` the caller's session is this script's session, so put
  # back what was changed. $global: is deliberate: a plain assignment inside a
  # function writes a local that dies with the call.
  $global:ErrorActionPreference = $PriorErrorAction
}

# A terminating error anywhere else would otherwise unwind past every
# Restore-CallerSession below and leave 'Stop' set in the caller's session.
trap {
  Restore-CallerSession
  break
}

# $PSCommandPath is the script's own path when it runs with -File, and empty when
# the text is executed in a session that already exists - which is what the
# documented `irm ... | iex` install does. `exit` there ends the console process
# itself, so exit only when there is a process of our own to exit.
$RanAsFile = [bool]$PSCommandPath

function Stop-WithError {
  param([string] $Message)
  Write-Host "X   $Message" -ForegroundColor Red
  Restore-CallerSession
  if ($RanAsFile) { exit 1 }
  throw 'install failed'
}

# Writes UTF-8 with no byte-order mark. Both parts matter: PowerShell 5.1's
# Set-Content -Encoding utf8 emits a BOM, which agent config parsers reject
# outright, and a CR that survives into a config value corrupts it silently -- an
# auth token with a trailing CR fails to authenticate with nothing reporting why.
function Write-TextFile {
  param([string] $Path, [string] $Content)
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
Write-Host 'Dash0 -> Amp telemetry installer' -ForegroundColor Cyan
Write-Host ''

$SourceDir = $env:DASH0_SOURCE_DIR

# At the top, unindented, and assigned a literal: this is the line
# scripts/verify-release-assets.sh parses to learn which assets this installer
# downloads, exactly as it does for the other agents' PowerShell bootstraps.
$Agent = 'amp'

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
# 2./3. Check tools and resolve the version. Neither is needed for a local
#       install, which works offline.
# ---------------------------------------------------------------------------

if (-not $SourceDir) {
  if (-not (Get-Command curl.exe -ErrorAction SilentlyContinue)) {
    Stop-WithError 'curl.exe not found (ships with Windows 10 1803 and later)'
  }

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
}

# ---------------------------------------------------------------------------
# 4. Resolve install paths. Amp reads plugins from ~/.config/amp/plugins/ on
#    every platform, and from <workspace>\.amp\plugins\ for a single project.
# ---------------------------------------------------------------------------

if ($Project) {
  $PluginDir = "$((Get-Location).Path)\.amp\plugins\dash0"
} else {
  $PluginDir = "$env:USERPROFILE\.config\amp\plugins\dash0"
}
$BinPath = "$PluginDir\amp-on-event.exe"
$IndexPath = "$PluginDir\index.ts"
# index.ts registers this by the same relative path, so the layout inside the
# plugin directory has to match the layout in the repository.
$SkillDir = "$PluginDir\skills\dash0-configure"
$SkillPath = "$SkillDir\SKILL.md"
$AmpDir = "$env:USERPROFILE\.amp"
$ConfigPath = "$AmpDir\dash0-agent-plugin.local.md"

foreach ($dir in @($PluginDir, $AmpDir)) {
  try {
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
  } catch {
    Stop-WithError "could not create $dir"
  }
}

# ---------------------------------------------------------------------------
# 5. Install the helper and index.ts. Both are replaced on every run, so
#    re-running really upgrades.
# ---------------------------------------------------------------------------

# Windows will not replace a file another process holds open, and a running turn
# holds the helper. What is on disk still runs, so name the one action that fixes
# it rather than abandon a half-done install.
function Move-IntoPlace {
  param([string] $Temp, [string] $Destination)
  try {
    Move-Item -LiteralPath $Temp -Destination $Destination -Force
  } catch {
    Remove-Item -LiteralPath $Temp -Force -ErrorAction SilentlyContinue
    Stop-WithError "could not replace $Destination (a running helper may hold it open) - quit Amp and re-run"
  }
  Write-Ok "installed -> $Destination"
}

if ($SourceDir) {
  if (-not (Test-Path -LiteralPath "$SourceDir\index.ts")) {
    Stop-WithError "no index.ts in $SourceDir"
  }
  if (-not (Test-Path -LiteralPath "$SourceDir\amp-on-event.exe")) {
    Stop-WithError "no amp-on-event.exe in $SourceDir (build it: go build -o $SourceDir\amp-on-event.exe .\cmd\amp-on-event)"
  }
  Copy-Item -LiteralPath "$SourceDir\index.ts" -Destination $IndexPath -Force
  Copy-Item -LiteralPath "$SourceDir\amp-on-event.exe" -Destination $BinPath -Force
  if (Test-Path -LiteralPath "$SourceDir\skills\dash0-configure\SKILL.md") {
    New-Item -ItemType Directory -Force -Path $SkillDir | Out-Null
    Copy-Item -LiteralPath "$SourceDir\skills\dash0-configure\SKILL.md" -Destination $SkillPath -Force
  }
  Write-Ok "installed from $SourceDir -> $PluginDir"
} else {
  $BaseUrl = "https://github.com/$Repo/releases/download/v$Version"
  $RawBase = "https://raw.githubusercontent.com/$Repo/v$Version"
  # Published per platform, installed under the plain name: amp/index.ts spawns
  # exactly .\amp-on-event.exe next to itself.
  #
  # Built from $Agent for the same reason as install-amp.sh: it is what
  # scripts/verify-release-assets.sh reads to know which assets to probe.
  $BinAsset = "$Agent-on-event-windows-$Arch.exe"

  Write-Info "downloading amp-on-event v$Version..."
  # Staged, not written straight to the destination: curl creates the file before
  # it learns the request failed, so a 404 would truncate a copy that works.
  $BinTmp = "$BinPath.tmp.$PID"
  & curl.exe -fsS -L -o $BinTmp "$BaseUrl/$BinAsset"
  if ($LASTEXITCODE -ne 0) {
    Remove-Item -LiteralPath $BinTmp -Force -ErrorAction SilentlyContinue
    Stop-WithError "failed to download the binary: $BaseUrl/$BinAsset (does v$Version publish Amp assets?)"
  }

  $Checksums = & curl.exe -fsS -L "$BaseUrl/checksums.txt"
  if ($LASTEXITCODE -ne 0) {
    Remove-Item -LiteralPath $BinTmp -Force -ErrorAction SilentlyContinue
    Stop-WithError "failed to download $BaseUrl/checksums.txt"
  }

  # Fail closed, like the shell installer: a binary that cannot be verified is
  # deleted rather than installed.
  $Expected = ''
  foreach ($Line in $Checksums) {
    $Fields = -split $Line
    if ($Fields.Length -eq 2 -and $Fields[1] -eq $BinAsset) { $Expected = $Fields[0] }
  }
  if (-not $Expected) {
    Remove-Item -LiteralPath $BinTmp -Force -ErrorAction SilentlyContinue
    Stop-WithError "no checksum for $BinAsset in v$Version - refusing to install an unverified binary"
  }
  # System.Security.Cryptography rather than Get-FileHash, which lives in
  # Microsoft.PowerShell.Utility and does not autoload in a 5.1 child that
  # inherited a PSModulePath listing PowerShell 7's module directories first.
  $Actual = [System.BitConverter]::ToString(
    [System.Security.Cryptography.SHA256]::Create().ComputeHash(
      [System.IO.File]::ReadAllBytes($BinTmp))).Replace('-', '')
  # -ne on strings is case-insensitive, which pairs the upper-case digest .NET
  # returns with the lower-case one in checksums.txt.
  if ($Actual -ne $Expected) {
    Remove-Item -LiteralPath $BinTmp -Force -ErrorAction SilentlyContinue
    Stop-WithError "checksum mismatch for $BinAsset (expected $Expected, got $Actual)"
  }
  Move-IntoPlace $BinTmp $BinPath

  # index.ts comes from the tagged ref rather than the release: it is source, not
  # a build artifact, and the tag carries the exact copy the helper was built
  # with. Same mechanism install-cursor.sh uses for its plugin files.
  Write-Info 'downloading index.ts...'
  $IndexTmp = "$IndexPath.tmp.$PID"
  & curl.exe -fsS -L -o $IndexTmp "$RawBase/amp/index.ts"
  if ($LASTEXITCODE -ne 0) {
    Remove-Item -LiteralPath $IndexTmp -Force -ErrorAction SilentlyContinue
    Stop-WithError "failed to download: $RawBase/amp/index.ts"
  }
  Move-IntoPlace $IndexTmp $IndexPath

  # The skill is optional: index.ts guards its registration, so a release that
  # predates it still installs a working bridge instead of failing here.
  $SkillTmp = "$PluginDir\skill.md.tmp.$PID"
  & curl.exe -fsS -L -o $SkillTmp "$RawBase/amp/skills/dash0-configure/SKILL.md"
  if ($LASTEXITCODE -eq 0) {
    New-Item -ItemType Directory -Force -Path $SkillDir | Out-Null
    Move-IntoPlace $SkillTmp $SkillPath
  } else {
    Remove-Item -LiteralPath $SkillTmp -Force -ErrorAction SilentlyContinue
    Write-Info "no dash0-configure skill in v$Version, continuing"
  }
}

# ---------------------------------------------------------------------------
# 6. Collect configuration. Precedence: parameter or env var, then prompt, then
#    skip with a warning.
# ---------------------------------------------------------------------------

function Read-Value {
  param([string] $Current, [string] $Label, [string] $Default = '')
  # Before the early return, so -Dataset/-Team and their environment variables
  # clear the key as the prompt does. install-amp.sh checks after collecting a
  # value for exactly this reason; returning here wrote a literal "-" instead,
  # which reaches Dash0 as a dataset header and a team name.
  if ($Current -eq '-') { return '' }
  if ($Current) { return $Current }
  if (-not [Environment]::UserInteractive -or [Console]::IsInputRedirected) { return $Default }
  if ($Default) {
    $answer = Read-Host "$Label [$Default]"
  } else {
    $answer = Read-Host $Label
  }
  if (-not $answer) { return $Default }
  if ($answer -eq '-') { return '' }
  return $answer
}

function Read-Secret {
  param([string] $Current, [string] $Label, [string] $Default = '')
  if ($Current) { return $Current }
  if (-not [Environment]::UserInteractive -or [Console]::IsInputRedirected) { return $Default }
  if ($Default) { $Label += ' [Enter to keep current]' }
  $secure = Read-Host "$Label (input hidden)" -AsSecureString
  $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
  try {
    $answer = [Runtime.InteropServices.Marshal]::PtrToStringAuto($bstr)
    if (-not $answer) { return $Default }
    return $answer
  } finally {
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
  }
}

$AgentName = 'amp'
# An existing config supplies the defaults - see install-amp.sh for why: this
# file is machine-wide and shared with every -Project install, so a re-run that
# answers nothing must not blank the token the other projects export with.
$ExtraConfig = @()
$ExistingConfig = @{ Frontmatter = @(); Body = '' }
if (Test-Path -LiteralPath $ConfigPath) {
  # Write-TextFile emits BOM-less UTF-8; PS 5.1's default ANSI read corrupts it.
  $existing = [IO.File]::ReadAllText($ConfigPath, [Text.Encoding]::UTF8)
  $parts = ([regex]'(?m)^[ \t]*---[ \t]*\r?$(?:\n)?').Split($existing, 3)
  if ($parts.Count -gt 1) {
    $ExistingConfig.Frontmatter = @($parts[1] -split '\r?\n')
    if ($parts.Count -gt 2) { $ExistingConfig.Body = $parts[2] }
  } else {
    $ExistingConfig.Body = $existing
  }
  # Keys this installer does not write belong to the user, export_usage above all.
  $ExtraConfig = @($ExistingConfig.Frontmatter | Where-Object { $_ -and $_ -notmatch '^\s*(otlp_url:|auth_token:|dataset:|agent_name:|team_name:)' })
}
function Get-ConfigValue {
  param([string] $Key)
  # Quotes optional - a hand-edited unquoted value must not be read as absent.
  $match = $ExistingConfig.Frontmatter | Select-String -Pattern "^\s*$Key`:\s*(.*)$" | Select-Object -First 1
  if ($match) { return $match.Matches[0].Groups[1].Value.Trim() -replace '^"(.*)"$', '$1' }
  return ''
}
$Endpoint = Read-Value $Endpoint 'Dash0 OTLP endpoint URL (e.g. https://ingress.<region>.aws.dash0.com)' (Get-ConfigValue 'otlp_url')
$Token = Read-Secret $Token 'Dash0 auth token' (Get-ConfigValue 'auth_token')
$DatasetDefault = Get-ConfigValue 'dataset'
if (-not (Test-Path -LiteralPath $ConfigPath)) { $DatasetDefault = 'default' }
$Dataset = Read-Value $Dataset 'Dash0 dataset (optional; - to clear)' $DatasetDefault
$Team = Read-Value $Team 'Team name (optional; - to clear)' (Get-ConfigValue 'team_name')

if (-not $Endpoint -or -not $Token) {
  Write-Warn 'OTLP URL or auth token not provided. The plugin installs but stays inactive.'
  Write-Warn "Re-run with -Endpoint and -Token, or edit $ConfigPath later."
}

# ---------------------------------------------------------------------------
# 7. Write the config file, restricted to this user: it holds the token in
#    cleartext. Always user scope, even with -Project: a workspace-level
#    .amp\dash0-agent-plugin.local.md is a token inside a Git working tree, and
#    that is a decision for whoever owns the repository, not for this installer.
# ---------------------------------------------------------------------------

$lines = @('---', "otlp_url: `"$Endpoint`"", "auth_token: `"$Token`"")
if ($Dataset) { $lines += "dataset: `"$Dataset`"" }
if ($AgentName) { $lines += "agent_name: `"$AgentName`"" }
if ($Team) { $lines += "team_name: `"$Team`"" }
$lines += $ExtraConfig
$lines += '---'
Write-TextFile $ConfigPath (($lines -join "`n") + "`n" + $ExistingConfig.Body)
Protect-File $ConfigPath
Write-Ok "wrote config -> $ConfigPath (owner only)"

# The token lives in the config file from here on. Under the documented
# `irm ... | iex` install the script's variables outlive the run, because the
# text executes in the caller's session, so all variables that carry the token
# are dropped rather than left reachable through Get-Variable.
Remove-Variable Token, lines, existing, parts, ExistingConfig, ExtraConfig -ErrorAction SilentlyContinue
Restore-CallerSession

# ---------------------------------------------------------------------------
# 8. Done.
# ---------------------------------------------------------------------------

Write-Host ''
Write-Host 'Next steps' -ForegroundColor Cyan
Write-Host "  1. Reload plugins from Amp's command palette (or start a new Amp session)."
Write-Host '  2. Run a prompt. Spans should land in your Dash0 dataset with gen_ai.harness.name=amp.'
Write-Host ''
Write-Host "Execute mode must wait for plugins to load: amp --plugin-ready-timeout 30 -x 'Your task'"
Write-Host 'An Orb needs its own installation - run the Linux installer there; a local install is not copied.'
# Token accounting is on with no option written to the config, so the one place a
# user learns it is running - and how it fails - is here.
Write-Host ''
Write-Host 'Token accounting is on' -ForegroundColor Cyan
Write-Host "  Each completed turn runs 'amp threads export' to read its models and token counts."
Write-Host '  It needs working Amp authentication here, and never delays a turn: the helper is detached.'
Write-Host '  If that read fails, the turn still exports, carrying dash0.amp.usage.status=unavailable instead of tokens.'
Write-Host "  To turn it off, add 'export_usage: false' between the --- markers in $ConfigPath."
Write-Host "To reconfigure later, edit $ConfigPath (a workspace .amp\dash0-agent-plugin.local.md outranks it; keep it out of Git)."
Write-Host 'To uninstall, run uninstall-amp.ps1.'
