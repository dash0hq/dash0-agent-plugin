# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

<#
.SYNOPSIS
Dash0 - Cursor telemetry uninstaller for Windows.

.DESCRIPTION
The Windows counterpart of uninstall-cursor.sh. ConvertFrom-Json replaces jq, so
unlike the shell version this never has to ask the user to edit hooks.json by
hand.

.EXAMPLE
powershell -ExecutionPolicy Bypass -File uninstall-cursor.ps1

.EXAMPLE
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/uninstall-cursor.ps1))) -Yes

.NOTES
What this removes:
  %USERPROFILE%\.cursor\plugins\local\dash0-agent-plugin\  entire plugin dir
  %USERPROFILE%\.cursor\hooks.json
      Dash0 entries only - any user-authored hooks in the same file are
      preserved. If no entries are left, the file is deleted.
  %USERPROFILE%\.local\state\dash0-agent-plugin\cursor\    binary cache
  %USERPROFILE%\.cursor\dash0-agent-plugin.local.md        credential config
#>

param(
  [switch] $Yes
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

function Write-Info { param([string] $Message) Write-Host $Message }
function Write-Ok { param([string] $Message) Write-Host "OK  $Message" -ForegroundColor Green }
function Write-Warn { param([string] $Message) Write-Host "!   $Message" -ForegroundColor Yellow }
function Stop-WithError {
  param([string] $Message)
  Write-Host "X   $Message" -ForegroundColor Red
  exit 1
}

# Writes UTF-8 with no byte-order mark and LF endings. See install-cursor.ps1 for
# why both matter.
function Write-TextFile {
  param([string] $Path, [string] $Content)
  $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($Path, ($Content -replace "`r`n", "`n"), $utf8NoBom)
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
# Resolve paths (must mirror install-cursor.ps1). Windows support starts with the
# release that added it, so there are no legacy layouts to clean up here.
# ---------------------------------------------------------------------------

$PluginDir = "$env:USERPROFILE\.cursor\plugins\local\dash0-agent-plugin"
$HooksPath = "$env:USERPROFILE\.cursor\hooks.json"
$ConfigPath = "$env:USERPROFILE\.cursor\dash0-agent-plugin.local.md"
if ($env:XDG_STATE_HOME) {
  $StateDir = "$env:XDG_STATE_HOME\dash0-agent-plugin\cursor"
} else {
  $StateDir = "$env:USERPROFILE\.local\state\dash0-agent-plugin\cursor"
}

Write-Host ''
Write-Host 'Dash0 -> Cursor telemetry uninstaller' -ForegroundColor Cyan
Write-Host ''
Write-Host 'Will remove (if present):'
Write-Host "  $PluginDir"
Write-Host "  $StateDir"
Write-Host "  $ConfigPath"
Write-Host "  $HooksPath (Dash0 entries only; user hooks preserved)"
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

Remove-InstalledPath $PluginDir 'plugin dir'
Remove-InstalledPath $StateDir 'binary cache'
Remove-InstalledPath $ConfigPath 'config file'

# ---------------------------------------------------------------------------
# Strip Dash0 entries from hooks.json, preserving any user-authored ones. The
# match is on `cursor-on-event` in the command, which covers both the PowerShell
# bootstrap this installer registers and a shell one left by an install under Git
# Bash. An event left with no entries is dropped; a file left with no events is
# deleted.
# ---------------------------------------------------------------------------

if (Test-Path -LiteralPath $HooksPath) {
  try {
    $existing = Get-Content -LiteralPath $HooksPath -Raw | ConvertFrom-Json
  } catch {
    Write-Warn "failed to parse $HooksPath (invalid JSON?) - leaving the file in place."
    $existing = $null
  }

  if ($existing) {
    $kept = [ordered]@{}
    if ($existing.PSObject.Properties['hooks']) {
      foreach ($property in $existing.hooks.PSObject.Properties) {
        $entries = @()
        foreach ($entry in @($property.Value)) {
          if ($entry.PSObject.Properties['command'] -and "$($entry.command)" -like '*cursor-on-event*') { continue }
          $entries += $entry
        }
        if ($entries.Count -gt 0) { $kept[$property.Name] = $entries }
      }
    }

    if ($kept.Count -eq 0) {
      Remove-Item -LiteralPath $HooksPath -Force
      Write-Ok "removed hooks (no user entries left) -> $HooksPath"
    } else {
      $output = [pscustomobject]@{ version = 1; hooks = [pscustomobject]$kept }
      Write-TextFile $HooksPath (($output | ConvertTo-Json -Depth 10) + "`n")
      Write-Ok "stripped Dash0 entries from $HooksPath ($($kept.Count) event(s) preserved)"
    }
  }
} else {
  Write-Info "skip hooks (not present): $HooksPath"
}

Write-Host ''
Write-Host 'Done.' -ForegroundColor Cyan
Write-Host 'Restart Cursor so it stops registering the hooks.'
