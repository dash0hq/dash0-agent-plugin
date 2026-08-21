# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Windows counterpart of codex-on-event.sh. Codex picks this file over the shell
# one through commandWindows in hooks.json, so each hook invocation runs:
#
#   stdin (JSON) -> codex-on-event.ps1 -> codex-on-event.exe -> OTLP
#
# Windows PowerShell 5.1 is the target: no $IsWindows, no ternary, no
# null-coalescing, no Join-Path with more than one child path.
#
# Fail-open: any error before running the binary writes to stderr and exits 0, so
# a broken install never breaks the user's Codex session.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$Agent = 'codex'
$Version = '0.1.24'

# Where the downloaded binary lives. Resolution order matches codex-on-event.sh:
# DASH0_PLUGIN_DATA, then PLUGIN_DATA (which Codex sets for a marketplace install,
# keeping the cache inside the plugin's own state), then the XDG path the
# installer uses.
if ($env:DASH0_PLUGIN_DATA) {
  $Base = $env:DASH0_PLUGIN_DATA
} elseif ($env:PLUGIN_DATA) {
  $Base = $env:PLUGIN_DATA
} elseif ($env:XDG_STATE_HOME) {
  $Base = "$env:XDG_STATE_HOME/dash0-agent-plugin/codex"
} else {
  $Base = "$env:USERPROFILE/.local/state/dash0-agent-plugin/codex"
}

# >>> shared bootstrap — byte-identical across the PowerShell bootstraps >>>
# test/consistency asserts these regions match, so a fix lands in all of them or
# in none. Everything agent-specific is declared above.

function Exit-FailOpen {
  param([string] $Message)
  [Console]::Error.WriteLine("$Agent-on-event: $Message")
  exit 0
}

$BinDir = "$Base/bin"
$Repo = 'dash0hq/dash0-agent-plugin'

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
  Exit-FailOpen "unsupported architecture: $Machine"
}

$Asset = "$Agent-on-event-windows-$Arch.exe"
$Binary = "$BinDir/$Agent-on-event-$Version-windows-$Arch.exe"

if (-not (Test-Path -LiteralPath $Binary)) {
  try {
    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
  } catch {
    Exit-FailOpen "could not create $BinDir"
  }

  $BaseUrl = "https://github.com/$Repo/releases/download/v$Version"

  # curl.exe, not Invoke-WebRequest: it ships with Windows 10 1803 and later,
  # while Invoke-WebRequest on 5.1 negotiates old TLS on older builds and pays for
  # a progress stream this has no use for.
  & curl.exe -fsS -L -o $Binary "$BaseUrl/$Asset"
  if ($LASTEXITCODE -ne 0) { Exit-FailOpen "download failed: $BaseUrl/$Asset" }

  $Checksums = & curl.exe -fsS -L "$BaseUrl/checksums.txt"
  if ($LASTEXITCODE -ne 0) { Exit-FailOpen "checksums fetch failed" }

  # Fail closed on integrity: a binary that cannot be verified is not run.
  # Get-FileHash is built in, so unlike the shell bootstraps there is no
  # missing-hash-tool case to refuse.
  $Expected = ''
  foreach ($Line in $Checksums) {
    $Fields = -split $Line
    if ($Fields.Length -eq 2 -and $Fields[1] -eq $Asset) { $Expected = $Fields[0] }
  }
  if (-not $Expected) {
    Remove-Item -LiteralPath $Binary -Force -ErrorAction SilentlyContinue
    Exit-FailOpen "no checksum for $Asset — refusing to run an unverified binary"
  }

  $Actual = (Get-FileHash -LiteralPath $Binary -Algorithm SHA256).Hash
  # -ne on strings is case-insensitive, which is what pairs Get-FileHash's
  # upper-case digest with the lower-case one in checksums.txt.
  if ($Actual -ne $Expected) {
    Remove-Item -LiteralPath $Binary -Force -ErrorAction SilentlyContinue
    Exit-FailOpen "checksum mismatch (expected $Expected, got $Actual)"
  }
}

# Run the binary as a native command with no pipeline, so it inherits this
# process's stdin. Piping instead would route the event JSON through PowerShell's
# formatter, and 5.1 re-encodes pipeline text — which corrupts every non-ASCII
# character in a prompt.
& $Binary @args
exit $LASTEXITCODE
# <<< shared bootstrap <<<
