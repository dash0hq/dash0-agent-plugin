// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// The driver is cross-platform; its stubs are not. Every fake REPL below is a
// shell or python script started by path, and CreateProcess runs an executable
// rather than a shebang. The ConPTY half is covered by the Windows arm of
// test/e2e, against the real TUIs.
//go:build !windows

package agentterm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/testenv"
)

// stubREPL writes a script behaving like the part of an agent CLI this driver
// touches: it proves it has a terminal, then echoes each line it is sent. Driving a
// real agent needs credentials, so pty allocation, line submission, draining and
// shutdown on EOT are pinned against this instead.
func stubREPL(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "stub-repl.sh")
	require.NoError(t, os.WriteFile(path, []byte(`#!/usr/bin/env bash
# A terminal is required, exactly as the real TUIs require one.
[ -t 0 ] || { echo "STUB: stdin is not a tty"; exit 1; }
echo "STUB READY term=$TERM"
while IFS= read -r line; do
  [ "$line" = "quit" ] && break
  echo "STUB ECHO: $line"
done
echo "STUB DONE"
`), 0o755))
	return path
}

// TestInteractiveDrivesARealTerminal pins the driver's mechanics: the child sees
// a tty, a Send arrives as a submitted line, and the output is drained where
// Expect can see it.
func TestInteractiveDrivesARealTerminal(t *testing.T) {
	cmd := exec.Command(stubREPL(t))
	cmd.Env = testenv.Clean()

	s := Start(t, t.Context(), cmd)
	defer s.Close()

	// A tty, not a pipe. The agents refuse to run their REPL otherwise.
	s.Expect("STUB READY", 10*time.Second)
	assert.NotContains(t, s.Output(), "stdin is not a tty")

	// TERM must be a real terminal type or TUIs bail out.
	s.Expect("term=xterm-256color", 5*time.Second)

	// Two sends, so ordering is covered rather than just the first one landing.
	s.Send("first prompt")
	s.Expect("STUB ECHO: first prompt", 10*time.Second)
	s.Send("second prompt")
	s.Expect("STUB ECHO: second prompt", 10*time.Second)
}

// TestSendSeparatesEnterFromText pins the fix for a prompt that never submitted.
// A TUI with bracketed paste treats one burst of bytes as pasted input, so text and
// its trailing Enter arriving together become a two-line paste. The stub reports
// the byte counts it receives, which is how this sees the split.
func TestSendSeparatesEnterFromText(t *testing.T) {
	// Raw mode matters: in canonical mode the line discipline holds input until a
	// terminator arrives and delivers the whole line in one read, hiding the thing
	// this checks. A real TUI runs raw, so the stub does too.
	script := filepath.Join(t.TempDir(), "reads.py")
	require.NoError(t, os.WriteFile(script, []byte(`#!/usr/bin/env python3
import os, sys, tty
tty.setraw(0)
sys.stdout.write("READER READY\r\n"); sys.stdout.flush()
while True:
    chunk = os.read(0, 4096)
    if not chunk:
        break
    sys.stdout.write("READ %d %s\r\n" % (len(chunk), "CR" if chunk == b"\r" else "TEXT"))
    sys.stdout.flush()
`), 0o755))

	cmd := exec.Command("python3", script)
	cmd.Env = testenv.Clean()

	restore := closeGrace
	closeGrace = 500 * time.Millisecond
	defer func() { closeGrace = restore }()

	s := Start(t, t.Context(), cmd)
	defer s.Close()
	s.Expect("READER READY", 10*time.Second)

	s.Send("hello world")

	// A single combined write would show one READ of 12 bytes and no lone CR.
	s.Expect("READ 11 TEXT", 10*time.Second)
	s.Expect("READ 1 CR", 10*time.Second)
	assert.NotContains(t, s.Output(), "READ 12",
		"the Enter must not share a read with the text, or the TUI takes it as a paste")
}

// TestInteractiveCloseStopsAnIgnoredProcess covers the shutdown path for a child
// that never exits on its own, which is the failure mode that would otherwise
// hang the whole e2e suite.
func TestInteractiveCloseStopsAnIgnoredProcess(t *testing.T) {
	script := filepath.Join(t.TempDir(), "ignores-eot.sh")
	require.NoError(t, os.WriteFile(script, []byte(`#!/usr/bin/env bash
trap '' INT TERM
echo "IGNORING READY"
while true; do sleep 1; done
`), 0o755))

	cmd := exec.Command(script)
	cmd.Env = testenv.Clean()

	// This asserts that the fallback happens, not how patient it is, and the real
	// value would add 15s to every `make test`.
	restore := closeGrace
	closeGrace = 500 * time.Millisecond
	defer func() { closeGrace = restore }()

	s := Start(t, t.Context(), cmd)
	s.Expect("IGNORING READY", 10*time.Second)

	start := time.Now()
	s.Close()
	assert.Less(t, time.Since(start), 10*time.Second,
		"Close must fall back to a kill rather than block on a child that ignores EOT")
}

// realTrustDialog is a byte-accurate excerpt of Claude Code's workspace-trust
// screen, from a failing run. Every gap between words is a cursor-position sequence
// rather than a space, and the DCS terminal-identity reply appeared mid-screen.
const realTrustDialog = "\x1b[2G\x1b[1mQuick\x1b[8Gsafety\x1b[15Gcheck:\x1b[22GIs\x1b[25Gthis\x1b[30Ga\x1b[32Gproject\x1b[40Gyou\x1b[44Gcreated\x1b[52Gor\x1b[55Gone\x1b[59Gyou\x1b[63Gtrust?\x1b[22m\r\n" +
	"\x1b[2G\xe2\x9d\xaf\x1b[4G1.\x1b[7GYes,\x1b[12GI\x1b[14Gtrust\x1b[20Gthis\x1b[25Gfolder\r\n" +
	"\x1bP>|xterm.js(6.0.0)\x1b\\\x1b[?1;2c"

// A TUI never emits the spaces a reader sees, so a needle written the way a human
// reads the screen has to survive normalization.
func TestExpectMatchesCursorPositionedText(t *testing.T) {
	// The literal a naive implementation looks for is genuinely absent.
	assert.NotContains(t, realTrustDialog, "trust this folder",
		"the spaced phrase must NOT be present, or this test is not exercising the problem")

	norm := normalizeTUI(realTrustDialog)
	for _, want := range []string{"trust this folder", "Quick safety check", "Yes, I trust this folder"} {
		assert.Contains(t, norm, normalizeTUI(want), "Expect must find %q on screen", want)
	}

	// The DCS reply must not leak into the matched text, or it splits a phrase.
	assert.NotContains(t, norm, "xterm.js(6.0.0)\x1b")
	assert.NotContains(t, stripEscapes(realTrustDialog), "\x1b")
}

// The same bytes through a live session, so the fix is covered end to end rather
// than only at the normalizer.
func TestExpectOnRealDialogViaSession(t *testing.T) {
	script := filepath.Join(t.TempDir(), "replay.sh")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/usr/bin/env bash\nprintf '%s' "+shellQuote(realTrustDialog)+"\nsleep 30\n"), 0o755))

	cmd := exec.Command(script)
	cmd.Env = testenv.Clean()

	restore := closeGrace
	closeGrace = 500 * time.Millisecond
	defer func() { closeGrace = restore }()

	s := Start(t, t.Context(), cmd)
	defer s.Close()

	s.Expect("trust this folder", 10*time.Second)
}

// shellQuote wraps s for bash single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestCloseKillsChildrenThatIgnoreHangup covers the orphan hardening, and the
// discriminating case is narrow. A child that honours SIGHUP dies on its own when
// the session leader goes; a child that called setsid is beyond any group-directed
// signal. In between sits this one: still in the group, ignoring SIGHUP, which is
// ordinary for a Node runtime.
func TestCloseKillsChildrenThatIgnoreHangup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "child-alive")

	script := filepath.Join(dir, "spawner.sh")
	require.NoError(t, os.WriteFile(script, []byte(`#!/usr/bin/env bash
trap '' HUP TERM INT
( trap '' HUP TERM INT
  while true; do touch `+shellQuote(marker)+`; sleep 0.2; done ) &
echo "SPAWNER READY"
while true; do sleep 1; done
`), 0o755))

	cmd := exec.Command(script)
	cmd.Env = testenv.Clean()

	restore := closeGrace
	closeGrace = 500 * time.Millisecond
	defer func() { closeGrace = restore }()

	s := Start(t, t.Context(), cmd)
	s.Expect("SPAWNER READY", 10*time.Second)
	require.Eventually(t, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}, 5*time.Second, 100*time.Millisecond, "the child never started")

	s.Close()

	require.NoError(t, os.Remove(marker))
	time.Sleep(1500 * time.Millisecond)
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err),
		"a child survived Close and is still running; it would keep writing after the test ends")
}

// The raw buffer carries sequences that reconfigure whatever terminal they are
// printed to, and Close logs the transcript, so Output must not return them.
func TestOutputIsSafeToLog(t *testing.T) {
	script := filepath.Join(t.TempDir(), "reconfigures.sh")
	// Mouse tracking, bracketed paste, alt-screen: the ones that leave a terminal
	// unusable when replayed into it.
	require.NoError(t, os.WriteFile(script, []byte(
		"#!/usr/bin/env bash\nprintf '\\033[?1004h\\033[?2004h\\033[?1049h\\033[1mBOOTED\\033[0m'\nsleep 30\n"), 0o755))

	cmd := exec.Command(script)
	cmd.Env = testenv.Clean()

	restore := closeGrace
	closeGrace = 500 * time.Millisecond
	defer func() { closeGrace = restore }()

	s := Start(t, t.Context(), cmd)
	defer s.Close()
	s.Expect("BOOTED", 10*time.Second)

	assert.Contains(t, s.raw(), "\x1b[?1004h", "the stub must really emit the sequences")
	assert.NotContains(t, s.Output(), "\x1b",
		"Output is logged by Close, so it must carry no escape sequence at all")
	assert.Contains(t, s.Output(), "BOOTED", "stripping must keep the text")
}

// Mark scopes a question to new output, which Expect cannot do: the transcript is
// cumulative, so a needle that appeared once matches forever.
func TestSeenSinceIgnoresEarlierOutput(t *testing.T) {
	s := &Session{t: t, done: make(chan struct{})}
	s.output.WriteString("Approaching rate limits\r\nSwitch to a cheaper model?\r\n")

	mark := s.Mark()
	require.True(t, s.SeenSince(0, "Approaching rate limits"),
		"from the start of the stream the dialog is there")
	assert.False(t, s.SeenSince(mark, "Approaching rate limits"),
		"after the mark it has not appeared again, which is the whole point")

	s.output.WriteString("Approaching rate limits\r\n")
	assert.True(t, s.SeenSince(mark, "Approaching rate limits"),
		"a second appearance is seen")
}

// A mark taken before output is truncated must not panic or match wildly.
func TestSeenSinceToleratesAnOutOfRangeMark(t *testing.T) {
	s := &Session{t: t, done: make(chan struct{})}
	s.output.WriteString("short")
	assert.False(t, s.SeenSince(9999, "short"), "a mark past the end sees nothing")
}

// realFlippedTrustDialog is Claude Code's folder-trust dialog captured on
// 2026-08-28. The preselected entry is "No, exit": the order flipped from an
// earlier release, which is what makes pressing Enter blind unsafe.
const realFlippedTrustDialog = "\x1b[2K\x1b[38;2;153;153;153m Quick safety check:\x1b[39m Is this a project you " +
	"created or one you trust?\r\n\x1b[2K\r\n\x1b[2K \x1b[38;2;215;119;87m❯\x1b[39m\x1b[38;2;215;119;87m" +
	"\x1b[13GNo,\x1b[17Gexit\x1b[39m\r\n\x1b[2K  \x1b[13GYes,\x1b[18GI\x1b[20Gtrust\x1b[26Gthis\x1b[31Gfolder\r\n"

// The marker names which entry is selected, and it is read from the LAST redraw
// because the transcript is cumulative.
func TestHighlightedReadsTheSelectedEntry(t *testing.T) {
	s := &Session{t: t, done: make(chan struct{})}
	s.output.WriteString(realFlippedTrustDialog)

	got := s.highlighted("Quick safety check")
	assert.True(t, strings.HasPrefix(got, normalizeTUI("No, exit")),
		"the dialog opens on the destructive entry; got %q", got)
	assert.False(t, strings.HasPrefix(got, normalizeTUI("Yes, I trust this folder")),
		"pressing Enter here would answer No")
}

// A marker drawn before the anchor belongs to something else on screen, such as
// the composer's own prompt, and must not read as a list selection.
func TestHighlightedIgnoresAMarkerBeforeTheAnchor(t *testing.T) {
	s := &Session{t: t, done: make(chan struct{})}
	s.output.WriteString("❯ typed into the composer\r\nQuick safety check: trust this?\r\n")

	assert.Empty(t, s.highlighted("Quick safety check"),
		"no selection marker after the anchor means no selection")
}

// Each agent draws its list differently: Claude has no entry numbers, Copilot
// numbers them and marks with "❯", Codex numbers them and marks with "›". The
// reader has to return the same label in all three.
func TestHighlightedHandlesEachAgentsListStyle(t *testing.T) {
	for _, tc := range []struct {
		name, screen, want string
	}{
		{"claude, no numbers", "Quick safety check\r\n ❯ No, exit\r\n   Yes, I trust this folder\r\n", "No, exit"},
		{"copilot, numbered", "Do you trust the files\r\n ❯ 1. Yes\r\n   2. Yes, and remember\r\n", "Yes"},
		{"codex, numbered with ›", "Do you trust the contents\r\n › 1. Yes, continue\r\n   2. No, quit\r\n", "Yes, continue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Session{t: t, done: make(chan struct{})}
			s.output.WriteString(tc.screen)
			anchor := strings.SplitN(tc.screen, "\r\n", 2)[0]
			assert.Equal(t, tc.want, s.highlighted(anchor))
		})
	}
}

// One option must never be mistaken for another that starts with it. Copilot offers
// "Yes" and "Yes, and remember this folder for future sessions", and the second
// records a temp directory in the developer's real config. The earlier table
// asserted a prefix, which held whichever entry carried the marker, so it passed
// against the bug.
func TestHighlightedDistinguishesAnOptionFromItsPrefix(t *testing.T) {
	const anchor = "Do you trust the files in this folder"
	screen := func(marked int) string {
		lines := []string{anchor, "   1. Yes", "   2. Yes, and remember this folder for future sessions"}
		lines[marked] = " ❯" + strings.TrimLeft(lines[marked], " ")
		return strings.Join(lines, "\r\n") + "\r\n"
	}

	s := &Session{t: t, done: make(chan struct{})}
	s.output.WriteString(screen(2))
	assert.Equal(t, "Yes, and remember this folder for future sessions", s.highlighted(anchor),
		"the longer entry is marked, and that is what must be reported")
	assert.NotEqual(t, "Yes", s.highlighted(anchor),
		"reporting the short entry here is how SelectOption would confirm the wrong one")

	s2 := &Session{t: t, done: make(chan struct{})}
	s2.output.WriteString(screen(1))
	assert.Equal(t, "Yes", s2.highlighted(anchor),
		"and the short entry is still recognised when it is the marked one")

	// The comparison SelectOption makes, not just the label it reads. Testing
	// highlighted() alone left the operator unguarded.
	assert.False(t, s.selects(anchor, "Yes"),
		"the longer entry is marked, so SelectOption must NOT confirm here")
	assert.True(t, s.selects(anchor, "Yes, and remember this folder for future sessions"),
		"it must confirm the entry that is actually marked")
	assert.True(t, s2.selects(anchor, "Yes"), "and the short entry when that one is marked")
}

// The entry number must not be mistaken for part of the label, or "Yes" never
// matches "1. Yes".
func TestHighlightedStripsTheEntryNumber(t *testing.T) {
	s := &Session{t: t, done: make(chan struct{})}
	s.output.WriteString("pick one\r\n ❯ 2. Yes, and remember this folder\r\n")
	got := s.highlighted("pick one")
	assert.False(t, strings.HasPrefix(got, "2."), "the ordinal is stripped; got %q", got)
	assert.True(t, strings.HasPrefix(got, "Yes, and remember"), "got %q", got)
}

// CSI sequences may carry a private parameter byte ("<", "=" or ">"), and a pattern
// allowing only digits, ";" and "?" leaves the whole sequence in the output. Close
// logs Output into the terminal running the test, so a missed class corrupts a real
// terminal on a passing run. "\x1b[>4;0m" appears in every captured transcript.
func TestStripEscapesHandlesPrivateCSIParameters(t *testing.T) {
	for _, raw := range []string{
		"\x1b[>4;0m",  // modifyOtherKeys, emitted by Claude Code and Copilot
		"\x1b[>4;2m",  // and its other form
		"\x1b[=3h",    // screen mode
		"\x1b[<35;1M", // SGR mouse report
		"\x1b[?2004h", // bracketed paste, already covered
		"\x1b[38;2;215;119;87m",
	} {
		assert.Equal(t, "AB", stripEscapes("A"+raw+"B"), "%q survived stripping", raw)
	}
}

// A redraw positions the cursor instead of emitting newlines, so two entries can
// share one physical line with no delimiter once escapes are stripped.
//
// The contract is then to match NOTHING rather than guess, because matching the
// leading entry would confirm whichever was drawn first. SelectOption presses Down,
// the dialog redraws on separate lines, and the next frame reads cleanly. Measured
// against Claude Code, whose first frame is concatenated and later ones are not.
func TestHighlightedHandlesAFrameDrawnWithoutNewlines(t *testing.T) {
	const anchor = "Quick safety check"
	s := &Session{t: t, done: make(chan struct{})}

	// Frame 1: one physical line, cursor-positioned, marker on the first entry.
	s.output.WriteString("Quick safety check: trust this?\r\n \x1b[38;5;1m❯\x1b[39m\x1b[13GNo,\x1b[17Gexit\x1b[30GYes,\x1b[35GI\x1b[37Gtrust\x1b[43Gthis\x1b[48Gfolder\r\n")
	assert.False(t, s.selects(anchor, "No, exit"),
		"an indivisible frame must not confirm the leading entry")
	assert.False(t, s.selects(anchor, "Yes, I trust this folder"),
		"nor the trailing one")

	// Frame 2: redrawn on two lines after a Down, marker now on the second entry.
	s.output.WriteString("  No, exit\r\n❯Yes, I trust this folder\r\n")
	assert.Equal(t, "Yes, I trust this folder", s.highlighted(anchor),
		"the newest frame wins, and its label is not concatenated with the other entry")
	assert.True(t, s.selects(anchor, "Yes, I trust this folder"),
		"so SelectOption confirms here")
}

// A numbered list IS divisible when it is drawn on one physical line, and both
// runtimes that number their entries redraw every frame that way. Waiting for a
// clean frame there never ends: SelectOption walks the whole list and gives up,
// which is how it failed on a runner against dialogs it answers by hand locally.
//
// Both strings below are captured from that run, escapes already stripped.
func TestHighlightedReadsANumberedListDrawnOnOneLine(t *testing.T) {
	// Copilot, mid-list. A partial redraw leaves a gap: the marked entry is 1 and
	// the only other one still on the line is 3.
	copilot := &Session{t: t, done: make(chan struct{})}
	copilot.output.WriteString("Do you trust the files in this folder?\r\n" +
		"╴╶    ╴╶ ❯ 1. Yes  3. No (Esc)▘▝ \r\n")
	const folder = "Do you trust the files in this folder"
	assert.Equal(t, "Yes", copilot.highlighted(folder),
		"the entry after the marked one must not be read as part of its label")
	assert.True(t, copilot.selects(folder, "Yes"), "so SelectOption confirms here")
	assert.False(t, copilot.selects(folder, "No (Esc)"), "and not the trailing entry")

	// The prefix hazard the ordinal cut must not reintroduce: "Yes" must not confirm
	// while the longer entry that starts with it is the marked one.
	longer := &Session{t: t, done: make(chan struct{})}
	longer.output.WriteString("Do you trust the files in this folder?\r\n" +
		"  1. Yes❯ 2. Yes, and remember this folder for future sessions\r\n")
	assert.False(t, longer.selects(folder, "Yes"),
		"the longer entry is marked, so confirming here picks the wrong one")
	assert.True(t, longer.selects(folder, "Yes, and remember this folder for future sessions"),
		"and the entry that is marked must still be recognised")

	// Codex, whole dialog on one line, marker on the first entry.
	codex := &Session{t: t, done: make(chan struct{})}
	codex.output.WriteString(">You are in /tmpDoyoutrustthecontentsofthisdirectory?" +
		"Workingwithuntrustedcontents.› 1. Yes, continue2.No,quitPress enter to continue\r\n")
	const dir = "Do you trust the contents of this directory"
	assert.Equal(t, "Yes, continue", codex.highlighted(dir),
		"the second entry and the footer must be cut at the entry number")
	assert.True(t, codex.selects(dir, "Yes, continue"), "so SelectOption confirms here")
}

// An unnumbered list drawn on one line with a space between the entries. Claude
// Code's API-key dialog reads exactly this under a ConPTY, on every frame, and the
// space is the only thing separating the two entries.
//
// The frames below are captured from a runner. Nothing here is Windows-specific to
// the reader, so the coverage belongs with the rest of it.
func TestHighlightedReadsAnUnnumberedListSeparatedByOneSpace(t *testing.T) {
	const anchor = "Do you want to use this API key"
	marked := func(frame string) *Session {
		s := &Session{t: t, done: make(chan struct{})}
		s.output.WriteString(anchor + "?\r\n" + frame + "\r\n")
		return s
	}

	on := marked("❯Yes No (recommended)")
	assert.True(t, on.selects(anchor, "Yes"),
		"the marked entry is Yes, and the entry sharing its line must not hide that")
	assert.False(t, on.selects(anchor, "No (recommended)"),
		"the trailing entry is not the marked one")

	off := marked(" Yes❯No (recommended)")
	assert.True(t, off.selects(anchor, "No (recommended)"),
		"and the other frame, where the marker sits between the two")
	assert.False(t, off.selects(anchor, "Yes"),
		"the entry BEFORE the marker is not selected")

	// The space rule must not reopen the prefix hazard. A comma is not a space, so
	// asking for the shorter label while the longer one is marked stays refused.
	longer := &Session{t: t, done: make(chan struct{})}
	longer.output.WriteString("pick one\r\n❯Yes, and remember this folder\r\n")
	assert.False(t, longer.selects("pick one", "Yes"),
		"confirming here would pick an entry that writes to the developer's real config")
}
