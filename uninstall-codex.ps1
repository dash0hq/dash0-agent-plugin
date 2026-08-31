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
    Restore-CallerSession
    # `return`, not `exit`: this is top-level script scope, so it ends the script
    # under -File and ends the executed text under `irm ... | iex`, where `exit`
    # would close the user's console for answering no to a prompt.
    return
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
    # -Encoding UTF8: without it, Get-Content reads a BOM-less config.toml under
    # the system's legacy codepage, corrupting any non-ASCII byte in the user's
    # own hooks/comments once it gets rewritten below.
    foreach ($line in (Get-Content -LiteralPath $ConfigToml -Encoding UTF8)) {
      if ($line.Contains($BeginMarker)) { $skip = $true }
      if (-not $skip) { $kept.Add($line) }
      if ($line.Contains($EndMarker)) { $skip = $false }
    }
    # Drop the separator blank line the installer left right before the marker.
    # It precedes the marker, so nothing marks it as "ours" to strip on its own.
    while ($kept.Count -gt 0 -and $kept[$kept.Count - 1] -eq '') {
      $kept.RemoveAt($kept.Count - 1)
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

Restore-CallerSession
