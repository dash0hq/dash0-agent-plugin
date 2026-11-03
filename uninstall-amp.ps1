# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

<#
.SYNOPSIS
Dash0 - Amp CLI and Orb telemetry uninstaller for Windows.

.DESCRIPTION
The Windows counterpart of uninstall-amp.sh.

.EXAMPLE
powershell -ExecutionPolicy Bypass -File uninstall-amp.ps1

.EXAMPLE
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/dash0hq/dash0-agent-plugin/main/uninstall-amp.ps1))) -Yes

.NOTES
What this removes:
  %USERPROFILE%\.config\amp\plugins\dash0-agent-plugin\
      The plugin directory (index.ts + helper). Only this directory; other Amp
      plugins stay.
  %USERPROFILE%\.amp\dash0-agent-plugin.local.md      credential config

-Project removes .\.amp\plugins\dash0-agent-plugin instead. A workspace
.amp\dash0-agent-plugin.local.md is left alone: the installer never wrote one.
#>

param(
  [switch] $Yes,
  [switch] $Project
)

Set-StrictMode -Version 2.0
# Saved so it can be put back - see uninstall-codex.ps1 for why.
$PriorErrorAction = $ErrorActionPreference
$ErrorActionPreference = 'Stop'

function Write-Info { param([string] $Message) Write-Host $Message }
function Write-Ok { param([string] $Message) Write-Host "OK  $Message" -ForegroundColor Green }
function Restore-CallerSession {
  $global:ErrorActionPreference = $PriorErrorAction
}

trap {
  Restore-CallerSession
  break
}

# `exit` would close the console under `irm ... | iex`, where this script has no
# process of its own - see uninstall-codex.ps1.
$RanAsFile = [bool]$PSCommandPath

function Stop-WithError {
  param([string] $Message)
  Write-Host "X   $Message" -ForegroundColor Red
  Restore-CallerSession
  if ($RanAsFile) { exit 1 }
  throw 'uninstall failed'
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

# Paths must mirror install-amp.ps1.
$UserConfigPath = "$env:USERPROFILE\.amp\dash0-agent-plugin.local.md"
if ($Project) {
  $PluginDir = "$((Get-Location).Path)\.amp\plugins\dash0-agent-plugin"
  # The config file is machine-wide and may hold the token for other projects,
  # so a project-scoped uninstall must not take it.
  $ConfigPath = ''
} else {
  $PluginDir = "$env:USERPROFILE\.config\amp\plugins\dash0-agent-plugin"
  $ConfigPath = $UserConfigPath
}

Write-Host ''
Write-Host 'Dash0 -> Amp telemetry uninstaller' -ForegroundColor Cyan
Write-Host ''
Write-Host 'Will remove (if present):'
Write-Host "  $PluginDir"
if ($ConfigPath) {
  Write-Host "  $ConfigPath"
} else {
  Write-Host "Leaving the user-level config in place: $UserConfigPath"
}
Write-Host ''

if (-not $Yes) {
  if (-not [Environment]::UserInteractive) {
    Stop-WithError 'not interactive; pass -Yes to proceed'
  }
  $reply = Read-Host 'Proceed? [y/N]'
  if ($reply -notmatch '^(y|Y|yes|YES)$') {
    Write-Info 'aborted'
    Restore-CallerSession
    # `return`, not `exit`: under `irm ... | iex` exit would close the user's
    # console for answering no to a prompt.
    return
  }
}

Remove-InstalledPath $PluginDir 'plugin directory'
# Installs before the rename used a sibling "dash0", and an uninstall that left
# it behind would keep exporting.
$StaleDir = Join-Path (Split-Path -Parent $PluginDir) 'dash0'
if ((Test-Path -LiteralPath "$StaleDir\index.ts") -and (Get-ChildItem -LiteralPath $StaleDir -Filter 'amp-on-event*' -ErrorAction SilentlyContinue)) {
  Remove-InstalledPath $StaleDir 'previous plugin directory'
}
if ($ConfigPath) { Remove-InstalledPath $ConfigPath 'config file' }

Write-Host ''
Write-Host 'Done.' -ForegroundColor Cyan
Write-Host 'Reload plugins in Amp (or start a new session) so it stops loading the plugin.'

Restore-CallerSession
