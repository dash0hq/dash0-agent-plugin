// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// The checks that read the shipped .ps1 files: the three bootstraps and the
// installers at the root. They run everywhere rather than behind a Windows suffix,
// because the drift they catch gets written on a laptop.

func (a Agent) powerShellRegion(t *testing.T) string {
	t.Helper()
	return sharedRegion(t, a.WindowsBootstrap, a.windowsBootstrapBody(t))
}

// The .ps1 side of TestShellBootstrapsShareOneImplementation.
func TestPowerShellBootstrapsShareOneImplementation(t *testing.T) {
	agents := windowsBootstraps(t)
	reference := agents[0].powerShellRegion(t)
	require.NotEmpty(t, strings.TrimSpace(reference))

	for _, a := range agents[1:] {
		assert.Equal(t, reference, a.powerShellRegion(t),
			"%s has diverged from %s inside the shared region; apply the change to all three",
			a.WindowsBootstrap, agents[0].WindowsBootstrap)
	}
}

// Each pair pins its own version, outside the shared region because the syntax
// differs. The version is in both the cache filename and the asset name, so drift
// leaves Windows fetching an asset that does not exist.
func TestBootstrapVersionsMatchAcrossPlatforms(t *testing.T) {
	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			assert.Equal(t, a.bootstrapVersion(t), a.powerShellVersion(t),
				"%s and %s pin different versions; bump both (scripts/version.sh does)",
				a.Bootstrap, a.WindowsBootstrap)
		})
	}
}

// Every .ps1 the repository ships: the installers and uninstallers at the root,
// plus each agent's bootstrap.
func powerShellFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(pluginrepo.Root(t), "*.ps1"))
	require.NoError(t, err)
	// Before the append, which would make the total non-empty whatever the glob
	// found, and a rename would shrink these checks in silence.
	require.Len(t, files, 4,
		"expected the cursor and codex install/uninstall scripts at the repo root; "+
			"if they moved, update this glob or nothing parses them")

	for _, a := range windowsBootstraps(t) {
		files = append(files, abs(t, a.WindowsBootstrap))
	}
	return files
}

// Nothing else parses these files, where the .sh side has shellcheck and `bash -n`,
// so a missing brace ships green in all three bootstraps at once and surfaces as a
// hook producing no output. powershell.exe where both exist, 5.1 being the target
// and the stricter parser.
//
// A developer with no PowerShell gets a skip; CI does not, this being the only
// thing on the ubuntu leg that reads these files as PowerShell.
func TestPowerShellFilesParse(t *testing.T) {
	shell := "pwsh"
	if runtime.GOOS == "windows" {
		shell = "powershell"
	}
	if _, err := exec.LookPath(shell); err != nil {
		require.Empty(t, os.Getenv("CI"),
			"%s is not on PATH; install it in this job, or nothing parses the .ps1 files", shell)
		t.Skipf("%s is not on PATH", shell)
	}

	for _, file := range powerShellFiles(t) {
		t.Run(filepath.Base(file), func(t *testing.T) {
			// Single-quoted, doubling any quote it contains: a double-quoted literal
			// would need a Windows path's backslashes escaped.
			literal := "'" + strings.ReplaceAll(file, "'", "''") + "'"
			script := "$e = $null; " +
				"[System.Management.Automation.Language.Parser]::ParseFile(" + literal + ", [ref]$null, [ref]$e) | Out-Null; " +
				"if ($e.Count) { $e | ForEach-Object { [Console]::Error.WriteLine($_) }; exit 1 }"
			out, err := exec.Command(shell, "-NoProfile", "-Command", script).CombinedOutput()
			assert.NoError(t, err, "%s reported parse errors:\n%s", shell, out)
		})
	}
}

// Windows PowerShell 5.1 reads a BOM-less .ps1 in the system's legacy codepage, so
// a multi-byte character is mis-decoded and can cascade into a parse error that
// shows up as a hook doing nothing. These files carry no BOM, so they stay ASCII.
func TestPowerShellFilesAreASCII(t *testing.T) {
	for _, file := range powerShellFiles(t) {
		t.Run(filepath.Base(file), func(t *testing.T) {
			body, err := os.ReadFile(file)
			require.NoError(t, err)
			for i, b := range body {
				require.Less(t, b, byte(0x80),
					"non-ASCII byte %#x at offset %d, line %d",
					b, i, 1+strings.Count(string(body[:i]), "\n"))
			}
		})
	}
}

// The PowerShell bootstraps read no version override, and that absence is what
// makes them safe rather than any validation of their own. The .sh side takes
// DASH0_VERSION into a download URL and a filesystem path, so it carries a regex
// guard; the same input here without the guard would let a Windows session fetch
// from whatever repository the variable names.
func TestPowerShellBootstrapsTakeNoVersionOverride(t *testing.T) {
	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			// Case-insensitive, because PowerShell resolves $env: names that way.
			var found []string
			for i, line := range strings.Split(a.windowsBootstrapBody(t), "\n") {
				if strings.Contains(strings.ToLower(line), "dash0_version") {
					found = append(found, fmt.Sprintf("%s:%d: %s", a.WindowsBootstrap, i+1, strings.TrimSpace(line)))
				}
			}
			assert.Empty(t, found,
				"%s now reads a version override:\n  %s\n\nThe .sh twin validates it "+
					"against ^[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9A-Za-z.]+)?$ because the value "+
					"reaches both a release URL and a path under BIN_DIR, and `..` in it "+
					"retargets the download at another repository whose checksums.txt then "+
					"verifies the binary. Add the same guard here and give it a behavioural "+
					"test, then relax this one.",
				a.WindowsBootstrap, strings.Join(found, "\n  "))
		})
	}
}

// PowerShell 5.1 is the floor, and what it rejects parses cleanly in 7, so
// TestPowerShellFilesParse passes these on a developer's pwsh and on the ubuntu
// leg while Windows Server is where they break. Textual, because asking 5.1 itself
// needs a 5.1 and this drift is written on a laptop.
//
// Two forbidden constructs are absent here for want of a safe textual rule. A `?`
// is not distinctive: install-*.ps1 use one inside a regex and Where-Object's `?`
// alias is idiomatic. A multi-child Join-Path is an arity rather than a token.
// Real 5.1 on the windows leg catches both, the first as a parse error and the
// second as a runtime binding error wherever the line executes.
func TestPowerShellFilesAvoidWhatFiveOneRejects(t *testing.T) {
	for _, bad := range []struct {
		// Matched literally, so it has to be a form that cannot appear innocently.
		pattern, instead string
	}{
		{"$IsWindows", "PowerShell 5.1 does not define it, so it is $null and every " +
			"test of it takes the wrong branch silently; test $env:OS or " +
			"[System.Environment]::OSVersion instead"},
		{"??", "the null-coalescing operator is 7.0; use an if or " +
			"[string]::IsNullOrEmpty"},
		{"?.", "null-conditional access arrived experimental in 7.0 and stable in " +
			"7.1; guard with an if"},
	} {
		t.Run(bad.pattern, func(t *testing.T) {
			for _, file := range powerShellFiles(t) {
				for i, line := range powerShellCodeLines(t, file) {
					assert.NotContains(t, line, bad.pattern,
						"%s:%d uses %s, which Windows PowerShell 5.1 rejects. %s\n\t%s",
						filepath.Base(file), i+1, bad.pattern, bad.instead, strings.TrimSpace(line))
				}
			}
		})
	}
}

// The file's lines with every comment blanked out and the count preserved, so an
// index is still a line number. Both comment forms (`#` in the bootstraps, `<# #>`
// in the installers), because a banner forbidding a construct names it, and raw
// text would flag the prohibition as the violation.
func powerShellCodeLines(t *testing.T, file string) []string {
	t.Helper()

	body, err := os.ReadFile(file)
	require.NoError(t, err)

	lines := strings.Split(string(body), "\n")
	out := make([]string, len(lines))
	inBlock := false
	for i, line := range lines {
		if inBlock {
			if j := strings.Index(line, "#>"); j >= 0 {
				inBlock = false
				line = line[j+2:]
			} else {
				continue // out[i] stays empty
			}
		}
		out[i] = codeOnly(line, &inBlock)
	}
	return out
}

// The comments dropped from one line. The `#` has to be outside a quote, a path or
// a format string being able to carry one. An unterminated `<#` sets inBlock for
// the caller.
func codeOnly(line string, inBlock *bool) string {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			b.WriteByte(c)
			// A backtick escapes the next character, so `" does not close the string
			// and flip the quote state for everything after it.
			if c == '`' && quote == '"' && i+1 < len(line) {
				i++
				b.WriteByte(line[i])
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '\'' || c == '"':
			quote = c
			b.WriteByte(c)
		case c == '<' && i+1 < len(line) && line[i+1] == '#':
			if j := strings.Index(line[i+2:], "#>"); j >= 0 {
				i += 2 + j + 1
				continue
			}
			*inBlock = true
			return b.String()
		case c == '#':
			return b.String()
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
