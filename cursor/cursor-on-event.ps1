# SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
# SPDX-License-Identifier: Apache-2.0

# Windows counterpart of cursor-on-event.sh. The installer registers this file by
# absolute path in ~/.cursor/hooks.json, which Cursor runs through PowerShell:
#
#   stdin (JSON) -> cursor-on-event.ps1 -> cursor-on-event.exe -> OTLP
#
# Windows PowerShell 5.1 is the target: no $IsWindows, no ternary, no
# null-coalescing, no Join-Path with more than one child path.
#
# Fail-open: any error before running the binary writes to stderr and exits 0, so
# a broken install never breaks the user's Cursor session.

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

$Agent = 'cursor'
$Version = '0.1.27'

# Where the downloaded binary lives, matching cursor-on-event.sh. internal/harness
# also honours CURSOR_PLUGIN_DATA; neither bootstrap does, on either platform.
if ($env:DASH0_PLUGIN_DATA) {
  $Base = $env:DASH0_PLUGIN_DATA
} elseif ($env:XDG_STATE_HOME) {
  $Base = "$env:XDG_STATE_HOME/dash0-agent-plugin/cursor"
} else {
  $Base = "$env:USERPROFILE/.local/state/dash0-agent-plugin/cursor"
}

# >>> shared bootstrap - byte-identical across the PowerShell bootstraps >>>
# test/consistency asserts these regions match, so a fix lands in all of them or
# in none. Everything agent-specific is declared above.

function Exit-FailOpen {
  param([string] $Message)
  [Console]::Error.WriteLine("$Agent-on-event: $Message")
  exit 0
}

# Fail open on the unforeseen too, which on this platform is most of it.
# Set-StrictMode with $ErrorActionPreference = 'Stop' turns any cmdlet or .NET
# failure into a terminating error, and an unhandled one exits non-zero: a
# Get-FileHash on a file the virus scanner still holds open, a Process.Start on a
# binary being scanned, an `& $Binary` whose file went away after Test-Path saw it.
# Cursor and Copilot both read a non-zero hook exit as a refusal, so trouble
# fetching telemetry would block the user's prompt or tool call. The shell twin
# leaves `set -e` off and routes everything through fail_open; this trap is how the
# same posture is reached here, and it covers the paths with no try of their own.
# The two deliberate `exit` calls below are unaffected, since an exit is not an
# error.
trap {
  [Console]::Error.WriteLine("$Agent-on-event: $_")
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

  # Download to a private temp and rename into place, as the shell bootstraps do.
  # Hooks run concurrently and every session on the machine shares this
  # directory, so writing $Binary directly let them interleave into one file; and
  # a failed transfer left a truncated file that every later hook fire would find
  # with Test-Path and run. $PID makes the name private, and each failure path
  # below removes it.
  $Tmp = "$Binary.tmp.$PID"

  # curl.exe, not Invoke-WebRequest: it ships with Windows 10 1803 and later,
  # while Invoke-WebRequest on 5.1 negotiates old TLS on older builds and pays for
  # a progress stream this has no use for.
  & curl.exe -fsS -L -o $Tmp "$BaseUrl/$Asset"
  if ($LASTEXITCODE -ne 0) {
    Remove-Item -LiteralPath $Tmp -Force -ErrorAction SilentlyContinue
    Exit-FailOpen "download failed: $BaseUrl/$Asset"
  }

  $Checksums = & curl.exe -fsS -L "$BaseUrl/checksums.txt"
  if ($LASTEXITCODE -ne 0) {
    Remove-Item -LiteralPath $Tmp -Force -ErrorAction SilentlyContinue
    Exit-FailOpen "checksums fetch failed"
  }

  # Fail closed on integrity: a binary that cannot be verified is not run.
  # Get-FileHash is built in, so unlike the shell bootstraps there is no
  # missing-hash-tool case to refuse.
  $Expected = ''
  foreach ($Line in $Checksums) {
    $Fields = -split $Line
    if ($Fields.Length -eq 2 -and $Fields[1] -eq $Asset) { $Expected = $Fields[0] }
  }
  if (-not $Expected) {
    Remove-Item -LiteralPath $Tmp -Force -ErrorAction SilentlyContinue
    Exit-FailOpen "no checksum for $Asset - refusing to run an unverified binary"
  }

  $Actual = (Get-FileHash -LiteralPath $Tmp -Algorithm SHA256).Hash
  # -ne on strings is case-insensitive, which is what pairs Get-FileHash's
  # upper-case digest with the lower-case one in checksums.txt.
  if ($Actual -ne $Expected) {
    Remove-Item -LiteralPath $Tmp -Force -ErrorAction SilentlyContinue
    Exit-FailOpen "checksum mismatch (expected $Expected, got $Actual)"
  }

  # Visible only once verified. Move-Item -Force replaces atomically within a
  # directory, so a concurrent hook sees either no file or a complete one.
  try {
    Move-Item -LiteralPath $Tmp -Destination $Binary -Force
  } catch {
    Remove-Item -LiteralPath $Tmp -Force -ErrorAction SilentlyContinue
    # Another hook winning the race is success, not failure: the file is there.
    if (-not (Test-Path -LiteralPath $Binary)) {
      Exit-FailOpen "could not move $Tmp into place"
    }
  }
}

# A harness can deliver the event on PowerShell's pipeline instead of this
# process's stdin, in which case it lands in $input and [Console]::In is empty.
# Cursor on Windows does exactly that: it writes the payload to a temp file and
# runs `Get-Content ... | & { $input | <command> }`. Without this branch the
# binary reads an empty stdin, fails open, and the harness reports the hook as
# producing no output.
#
# The bytes go to the child's stdin directly rather than through a PowerShell
# pipeline: 5.1 re-encodes pipeline text through $OutputEncoding, which is ASCII
# by default and mangles every non-ASCII character in a prompt.
# ProcessStartInfo.StandardInputEncoding would be the tidy fix, but it does not
# exist on .NET Framework, so write the encoded bytes to the raw stream.
# -join, not Out-String: Out-String renders through the formatter at the host's
# width, and a hook payload is one long line, so a wrap landing inside a JSON
# string value would make the event unparseable. [char]10 is LF, spelled without
# a backtick escape so the e2e twin of this wrapper can live in a Go raw string.
$Payload = (@($input) -join ([string][char]10))
if ($Payload) {
  $Psi = New-Object System.Diagnostics.ProcessStartInfo
  $Psi.FileName = $Binary
  $Quoted = @()
  foreach ($Arg in $args) {
    if ($Arg -match '\s') { $Quoted += """$Arg""" } else { $Quoted += $Arg }
  }
  $Psi.Arguments = ($Quoted -join ' ')
  $Psi.UseShellExecute = $false
  $Psi.RedirectStandardInput = $true
  $Proc = [System.Diagnostics.Process]::Start($Psi)
  $Bytes = [System.Text.Encoding]::UTF8.GetBytes($Payload)
  $Proc.StandardInput.BaseStream.Write($Bytes, 0, $Bytes.Length)
  $Proc.StandardInput.BaseStream.Flush()
  $Proc.StandardInput.Close()
  $Proc.WaitForExit()
  exit $Proc.ExitCode
}

# Otherwise run the binary as a native command with no pipeline, so it inherits
# this process's stdin - how every other harness delivers the event.
& $Binary @args
exit $LASTEXITCODE
# <<< shared bootstrap <<<
