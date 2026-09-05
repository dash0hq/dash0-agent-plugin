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

// The contracts over the .ps1 files this repo ships: the three bootstraps and the
// installers at the root.
//
// These read the files, so they run everywhere rather than behind a Windows
// suffix. The drift they catch gets written on a laptop.
// bootstrap_windows_test.go and test/installers execute the same files, and only
// on Windows.

// powerShellRegion is the shared region of an agent's Windows bootstrap.
func (a Agent) powerShellRegion(t *testing.T) string {
	t.Helper()
	return sharedRegion(t, a.WindowsBootstrap, a.windowsBootstrapBody(t))
}

// The PowerShell bootstraps carry one implementation, and their banner says so.
// The .sh checks read only <agent>-on-event.sh, so this is the .ps1 side's.
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
// differs. scripts/version.sh bumps all thirteen pins together, but a hand edit
// can move one and not the other. The version is in both the cache filename and
// the asset name, so drift makes Windows fetch an asset that does not exist.
func TestBootstrapVersionsMatchAcrossPlatforms(t *testing.T) {
	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			assert.Equal(t, a.bootstrapVersion(t), a.powerShellVersion(t),
				"%s and %s pin different versions; bump both (scripts/version.sh does)",
				a.Bootstrap, a.WindowsBootstrap)
		})
	}
}

// powerShellFiles is every .ps1 the repository ships: the installers and
// uninstallers at the root, plus each agent's bootstrap.
func powerShellFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(pluginrepo.Root(t), "*.ps1"))
	require.NoError(t, err)
	// Asserted before the bootstraps are appended, since that append makes the
	// total non-empty whatever the glob found. A rename would otherwise shrink the
	// checks in silence.
	require.Len(t, files, 4,
		"expected the cursor and codex install/uninstall scripts at the repo root; "+
			"if they moved, update this glob or nothing parses them")

	for _, a := range windowsBootstraps(t) {
		files = append(files, abs(t, a.WindowsBootstrap))
	}
	return files
}

// Nothing else parses these files. The .sh side has shellcheck and `bash -n`. A
// missing brace here ships green in all three bootstraps at once and surfaces as
// a hook producing no output.
//
// powershell.exe where both exist, since 5.1 is the target and the stricter
// parser. ParseFile reports every error rather than the first.
//
// A developer with no PowerShell gets a skip. CI does not, because on the ubuntu
// leg this is the only thing reading these files as PowerShell. macOS is the
// exception: macos-latest ships no pwsh and installing one costs a brew cask per
// run, for a check the other two legs already made. Add that install step and the
// darwin case can go.
func TestPowerShellFilesParse(t *testing.T) {
	shell := "pwsh"
	if runtime.GOOS == "windows" {
		shell = "powershell"
	}
	if _, err := exec.LookPath(shell); err != nil {
		require.True(t, os.Getenv("CI") == "" || runtime.GOOS == "darwin",
			"%s is not on PATH; install it in this job, or nothing parses the .ps1 files", shell)
		t.Skipf("%s is not on PATH", shell)
	}

	for _, file := range powerShellFiles(t) {
		t.Run(filepath.Base(file), func(t *testing.T) {
			// Single-quoted literal, doubling any quote it contains. A
			// double-quoted one would need a Windows path's backslashes escaped.
			literal := "'" + strings.ReplaceAll(file, "'", "''") + "'"
			script := "$e = $null; " +
				"[System.Management.Automation.Language.Parser]::ParseFile(" + literal + ", [ref]$null, [ref]$e) | Out-Null; " +
				"if ($e.Count) { $e | ForEach-Object { [Console]::Error.WriteLine($_) }; exit 1 }"
			out, err := exec.Command(shell, "-NoProfile", "-Command", script).CombinedOutput()
			assert.NoError(t, err, "%s reported parse errors:\n%s", shell, out)
		})
	}
}

// Windows PowerShell 5.1 reads a BOM-less .ps1 using the system's legacy codepage,
// so a multi-byte character is mis-decoded and can cascade into a parse error that
// shows up as a hook doing nothing. These files carry no BOM, so they stay ASCII.
// This has regressed three times.
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

// The PowerShell bootstraps read no version override. That absence is what makes
// them safe, rather than any validation of their own.
//
// The .sh side takes DASH0_VERSION, which reaches a download URL and a filesystem
// path, so it carries a regex guard that
// TestBootstrapsRefuseAVersionOverrideThatIsNotAVersion drives. Adding the same
// input here without the guard would let a Windows session fetch from whatever
// repository the variable names, and this test is the only place that would say
// so.
func TestPowerShellBootstrapsTakeNoVersionOverride(t *testing.T) {
	for _, a := range windowsBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			// Case-insensitive, because PowerShell resolves $env: names that way.
			// Reports the matching lines rather than the 200-line body.
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

// PowerShell 5.1 is the floor, and the constructs it rejects parse cleanly in 7.
// TestPowerShellFilesParse therefore passes them on a developer's pwsh and on the
// ubuntu leg, and Windows Server is where they break.
//
// Textual on purpose. Asking 5.1 itself needs a 5.1, which only windows-latest
// has, and this drift is written on a laptop.
//
// Two constructs the banners forbid are absent here, because no safe textual rule
// exists for them. A `?` is not distinctive enough to match: `install-*.ps1` use
// one inside a regex, and `Where-Object`'s `?` alias is idiomatic. `Join-Path`
// with several child paths is an arity rather than a token, so deciding it means
// parsing an argument list, which string scanning does not do reliably enough for
// four call sites.
//
// Real 5.1 on the windows leg catches both. A ternary is a parse error. A
// three-child Join-Path is a runtime binding error, so it fails wherever the line
// executes, which test/installers and bootstrap_windows_test.go between them
// cover. A call on a line neither reaches is unguarded, and that is the accepted
// cost.
func TestPowerShellFilesAvoidWhatFiveOneRejects(t *testing.T) {
	for _, bad := range []struct {
		// pattern is matched literally, so it must be a form that cannot appear
		// innocently in these files.
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
					// Reports the offending line, since the fix is local and these files
					// are long.
					assert.NotContains(t, line, bad.pattern,
						"%s:%d uses %s, which Windows PowerShell 5.1 rejects. %s\n\t%s",
						filepath.Base(file), i+1, bad.pattern, bad.instead, strings.TrimSpace(line))
				}
			}
		})
	}
}

// powerShellCodeLines returns the file's lines with every comment blanked out and
// the line count preserved, so an index is still a line number.
//
// Both comment forms, because a banner that forbids a construct names it. The
// bootstraps use `#` lines and the install scripts a `<# #>` block. Reading the
// raw text would flag the prohibition as the violation.
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

// codeOnly drops the comments from one line. The `#` has to be outside a quote,
// since a path or a format string can carry one, and a backtick inside a
// double-quoted string escapes the character after it. An unterminated `<#` sets
// inBlock for the caller.
func codeOnly(line string, inBlock *bool) string {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			b.WriteByte(c)
			// A backtick escapes the next character, so `" does not close the string.
			// An odd number of them would otherwise flip the quote state, and a later
			// # would read as string content.
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
				i += 2 + j + 1 // skip past the closing #>
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
