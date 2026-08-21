# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

<#
.SYNOPSIS
Dash0 - OpenAI Codex telemetry uninstaller for Windows.

.DESCRIPTION
The Windows counterpart of uninstall-codex.sh.

.EXAMPLE
powershell -ExecutionPolicy Bypass -File uninstall-codex.ps1

.EXAMPLE
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/uninstall-codex.ps1))) -Yes

.NOTES
What this removes:
  %USERPROFILE%\.codex\config.toml
      The managed block ONLY (the hooks + trust entries the plugin added,
      between its markers). User-authored hooks and all other config are
      preserved. If the file ends up empty, it is deleted.
  %USERPROFILE%\.codex\dash0-agent-plugin.local.md      credential config
  %USERPROFILE%\.local\state\dash0-agent-plugin\codex\  binary cache + bootstrap
#>

param(
  [switch] $Yes
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Write-Info { param([string] $Message) Write-Host $Message }
function Write-Ok { param([string] $Message) Write-Host "OK  $Message" -ForegroundColor Green }
function Stop-WithError {
  param([string] $Message)
  Write-Host "X   $Message" -ForegroundColor Red
  exit 1
}

# Writes UTF-8 with no byte-order mark, keeping the line endings as given. See
# install-codex.ps1 for why the BOM matters.
function Write-TextFile {
  param([string] $Path, [string] $Content)
  $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($Path, $Content, $utf8NoBom)
}

function Remove-InstalledPath {
  param([string] $Path, [string] $Label)
  if (Test-Path -LiteralPath $Path) {
    Remove-Item -LiteralPath $Path -Recurse -Force
    Write-Ok "removed $Label -> $Path"
  } else {
    Write-Info "skip $Label (not present): $Path"
  }
}

# ---------------------------------------------------------------------------
# Resolve paths (must mirror install-codex.ps1). Windows support starts with the
# release that added it, so there are no legacy layouts to clean up here.
# ---------------------------------------------------------------------------

$ConfigToml = "$env:USERPROFILE\.codex\config.toml"
$ConfigPath = "$env:USERPROFILE\.codex\dash0-agent-plugin.local.md"
if ($env:XDG_STATE_HOME) {
  $StateDir = "$env:XDG_STATE_HOME\dash0-agent-plugin\codex"
} else {
  $StateDir = "$env:USERPROFILE\.local\state\dash0-agent-plugin\codex"
}

Write-Host ''
Write-Host 'Dash0 -> OpenAI Codex telemetry uninstaller' -ForegroundColor Cyan
Write-Host ''
Write-Host 'Will remove (if present):'
Write-Host "  $ConfigToml (managed block only; user hooks + config preserved)"
Write-Host "  $ConfigPath"
Write-Host "  $StateDir"
Write-Host ''

if (-not $Yes) {
  if (-not [Environment]::UserInteractive) {
    Stop-WithError 'not interactive; pass -Yes to proceed'
  }
  $reply = Read-Host 'Proceed? [y/N]'
  if ($reply -notmatch '^(y|Y|yes|YES)$') {
    Write-Info 'aborted'
    exit 0
  }
}

# ---------------------------------------------------------------------------
# Strip the managed block from config.toml, preserving everything else.
# ---------------------------------------------------------------------------

$BeginMarker = '>>> dash0-agent-plugin (managed)'
$EndMarker = '<<< dash0-agent-plugin (managed)'

if (Test-Path -LiteralPath $ConfigToml) {
  $raw = [System.IO.File]::ReadAllText($ConfigToml)
  if ($raw.Contains($BeginMarker)) {
    # config.toml belongs to the user, so keep the line endings it already uses.
    $eol = "`r`n"
    if (-not $raw.Contains("`r`n")) { $eol = "`n" }

    $kept = New-Object System.Collections.Generic.List[string]
    $skip = $false
    foreach ($line in (Get-Content -LiteralPath $ConfigToml)) {
      if ($line.Contains($BeginMarker)) { $skip = $true }
      if (-not $skip) { $kept.Add($line) }
      if ($line.Contains($EndMarker)) { $skip = $false }
    }
    # Nothing but whitespace left means the file only ever held our block.
    if (($kept -join '').Trim()) {
      Write-TextFile $ConfigToml (($kept -join $eol) + $eol)
      Write-Ok "stripped managed block from $ConfigToml"
    } else {
      Remove-Item -LiteralPath $ConfigToml -Force
      Write-Ok "removed $ConfigToml (empty after strip)"
    }
  } else {
    Write-Info "skip config.toml (no managed block): $ConfigToml"
  }
} else {
  Write-Info "skip config.toml (not present): $ConfigToml"
}

Remove-InstalledPath $ConfigPath 'config file'
Remove-InstalledPath $StateDir 'binary cache + bootstrap'

Write-Host ''
Write-Host 'Done.' -ForegroundColor Cyan
Write-Host 'Start a new Codex session so it stops running the hooks.'
