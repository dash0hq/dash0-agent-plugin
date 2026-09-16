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
  %USERPROFILE%\.config\amp\plugins\dash0\
      The plugin directory (index.ts + helper). Only this directory; other Amp
      plugins stay.
  %USERPROFILE%\.amp\dash0-agent-plugin.local.md      credential config

-Project removes .\.amp\plugins\dash0 instead. A workspace
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
if ($Project) {
  $PluginDir = "$((Get-Location).Path)\.amp\plugins\dash0"
} else {
  $PluginDir = "$env:USERPROFILE\.config\amp\plugins\dash0"
}
$ConfigPath = "$env:USERPROFILE\.amp\dash0-agent-plugin.local.md"

Write-Host ''
Write-Host 'Dash0 -> Amp telemetry uninstaller' -ForegroundColor Cyan
Write-Host ''
Write-Host 'Will remove (if present):'
Write-Host "  $PluginDir"
Write-Host "  $ConfigPath"
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
Remove-InstalledPath $ConfigPath 'config file'

Write-Host ''
Write-Host 'Done.' -ForegroundColor Cyan
Write-Host 'Reload plugins in Amp (or start a new session) so it stops loading the plugin.'

Restore-CallerSession
