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
# Saved so it can be put back. The documented `irm ... | iex` install runs this
# text in the caller's session, so 'Stop' would be their setting for as long as
# that window stays open, and their next non-terminating error would abort a
# pipeline that used to survive it. Restored on the way out and in Stop-WithError,
# the two ways this script ends. Set-StrictMode has no such treatment: 5.1 offers
# no way to read the caller's setting back, and -Off could remove a safety net
# they chose deliberately.
$PriorErrorAction = $ErrorActionPreference
$ErrorActionPreference = 'Stop'

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
  throw 'uninstall failed'
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
    Restore-CallerSession
    # `return`, not `exit`: this is top-level script scope, so it ends the script
    # under -File and ends the executed text under `irm ... | iex`, where `exit`
    # would close the user's console for answering no to a prompt.
    return
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
    # -Encoding UTF8: without it, Get-Content reads a BOM-less JSON file under the
    # system's legacy codepage, corrupting any non-ASCII byte before
    # ConvertFrom-Json ever sees it.
    $existing = Get-Content -LiteralPath $HooksPath -Raw -Encoding UTF8 | ConvertFrom-Json
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
      # Same array shape the installer writes: cast each value, and hand the
      # object to ConvertTo-Json through -InputObject so a single remaining entry
      # is not unwrapped into a bare object Cursor will not read as a list.
      foreach ($name in @($kept.Keys)) {
        $kept[$name] = [object[]]@($kept[$name])
      }
      $output = [pscustomobject]@{ version = 1; hooks = [pscustomobject]$kept }
      Write-TextFile $HooksPath ((ConvertTo-Json -InputObject $output -Depth 10) + "`n")
      Write-Ok "stripped Dash0 entries from $HooksPath ($($kept.Count) event(s) preserved)"
    }
  }
} else {
  Write-Info "skip hooks (not present): $HooksPath"
}

Write-Host ''
Write-Host 'Done.' -ForegroundColor Cyan
Write-Host 'Restart Cursor so it stops registering the hooks.'

Restore-CallerSession
