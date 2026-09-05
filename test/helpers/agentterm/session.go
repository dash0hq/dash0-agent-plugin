// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package agentterm drives a coding-agent CLI through its real terminal REPL,
// so a prompt arrives the way a user types it and a first-run dialog can be
// answered.
//
// Nothing here knows what a turn is. Waiting for one belongs to otlpcapture,
// because the plugin's own chat span is the only agent-agnostic "turn finished"
// signal.
package agentterm

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/require"
)

// Terminal geometry handed to the child. A zero-size terminal makes some TUIs
// draw nothing and others exit.
const (
	ptyCols = 120
	ptyRows = 40
)

// pollInterval is how often the waiters re-check. Turns take tens of seconds, so
// this only bounds how quickly a satisfied wait returns.
const pollInterval = 250 * time.Millisecond

// sendSettle separates typed text from the Enter that submits it, so the TUI's
// paste detection does not swallow the Enter into the pasted block. A var so a
// test can shorten it.
var sendSettle = 400 * time.Millisecond

// closeGrace is how long Close waits for the REPL to honour EOT before killing
// it. A var so the test covering the kill path need not burn the real grace
// period on every `make test`.
var closeGrace = 15 * time.Second

// Session is one live agent CLI attached to a pseudo-terminal.
//
// Scraping the TUI for "the agent finished" would mean matching a prompt marker
// through ANSI redraws, which is per-agent and brittle, so nothing here tries.
// Callers wait on otlpcapture.Capture.WaitForChatSpans instead.
type Session struct {
	t   *testing.T
	tty pty.Pty
	cmd *pty.Cmd

	mu     sync.Mutex
	output bytes.Buffer

	done chan struct{}
}

// Start spawns cmd attached to a pseudo-terminal and begins draining
// its output. The caller must Close it.
//
// cmd carries Path, Args, Env and Dir but is not started here: a ConPTY attaches
// at spawn time, so go-pty creates the child and the context comes in separately.
// TERM is forced, because some TUIs refuse to start on an empty or "dumb" value.
func Start(t *testing.T, ctx context.Context, cmd *exec.Cmd) *Session {
	t.Helper()

	tty, err := pty.New()
	require.NoError(t, err, "allocating a pty for %s", cmd.Path)
	require.NoError(t, tty.Resize(ptyCols, ptyRows), "sizing the pty")

	child := tty.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	child.Env = append(append([]string{}, cmd.Env...), "TERM=xterm-256color")
	child.Dir = cmd.Dir
	require.NoError(t, child.Start(), "starting %s under a pty", cmd.Path)

	s := &Session{t: t, tty: tty, cmd: child, done: make(chan struct{})}

	// Drain continuously. Without this the child blocks once the pty buffer fills,
	// which for a redrawing TUI happens almost immediately.
	go func() {
		defer close(s.done)
		buf := make([]byte, 4096)
		for {
			n, readErr := tty.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.output.Write(buf[:n])
				s.mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	return s
}

// Send types one line into the session and presses Enter.
//
// Text and carriage return go in separately, with a pause between them. These TUIs
// enable bracketed paste, so a trailing \r in the same write lands in the composer
// as a second line instead of submitting. \r rather than \n because the agents run
// their terminal raw, with no line discipline to translate.
func (s *Session) Send(line string) {
	s.t.Helper()

	if line != "" {
		_, err := s.tty.Write([]byte(line))
		require.NoError(s.t, err, "typing into the pty")
		time.Sleep(sendSettle)
	}
	_, err := s.tty.Write([]byte("\r"))
	require.NoError(s.t, err, "submitting the line")
}

// Output returns everything the session has printed so far, with escape sequences
// removed, so it is safe to log. The raw buffer holds the TUI's terminal
// reconfiguration, and printing it applies all of it to the terminal running the
// test. Callers matching on the bytes use the unexported raw.
func (s *Session) Output() string {
	return stripEscapes(s.raw())
}

// raw returns the undecoded buffer. Never log this.
func (s *Session) raw() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.String()
}

// Expect blocks until the output contains want, and fails with the transcript if
// it does not arrive in time.
//
// Matching runs on a normalized view: escapes removed and all whitespace dropped
// from both sides. A TUI lays words out by moving the cursor rather than emitting
// spaces, so "Yes, I trust this folder" arrives interleaved with positioning
// sequences and the literal string is never present to match.
//
// Use it for what only a real terminal session meets, such as a folder-trust
// prompt. To detect the end of a turn, wait on Capture.WaitForChatSpans.
func (s *Session) Expect(want string, timeout time.Duration) {
	s.t.Helper()

	needle := normalizeTUI(want)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(normalizeTUI(s.raw()), needle) {
			return
		}
		time.Sleep(pollInterval)
	}
	s.t.Fatalf("timed out after %s waiting for %q in the session output.\n"+
		"--- transcript (escape sequences stripped) ---\n%s",
		timeout, want, s.Output())
}

// escapeSequence matches what a TUI interleaves with its text: CSI (cursor moves,
// colors), OSC (window titles), DCS (the terminal-identity reply that shows up in
// a transcript as ESC P > | xterm.js ... ESC \), and the two-byte escapes.
var escapeSequence = regexp.MustCompile(
	`\x1b\[[0-9;:<=>?]*[ -/]*[@-~]` + // CSI, private parameters included
		`|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)` + // OSC
		`|\x1b[P^_][^\x1b]*\x1b\\` + // DCS / PM / APC
		`|\x1b[()][0-9A-Za-z]` + // charset selection
		`|\x1b[=>78MDEHc]`) // keypad, save/restore, misc

// stripEscapes removes escape sequences, leaving text a human can read. Line
// structure survives, so a failure message still looks like a screen.
func stripEscapes(s string) string {
	return escapeSequence.ReplaceAllString(s, "")
}

// collapseTUI is normalizeTUI with single spaces kept: escapes removed, then every
// run of whitespace reduced to one space and the ends trimmed.
//
// For comparing one entry of a list rather than searching a screen. A redraw can put
// two entries on one physical line with only a space between them, and normalizeTUI
// drops the only thing that separates them; see selects.
func collapseTUI(s string) string {
	return strings.Join(strings.Fields(stripEscapes(s)), " ")
}

// normalizeTUI reduces terminal output to something a human-written needle can be
// compared against: escapes removed, then every space, tab and newline dropped.
// Dropping whitespace is what makes cursor-positioned layout matchable.
func normalizeTUI(s string) string {
	var b strings.Builder
	for _, r := range stripEscapes(s) {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Close ends the session: EOT asks the REPL to exit, and the process is killed if
// it ignores that. The transcript is logged either way, because it is the only
// record of what the agent was doing when an assertion failed.
func (s *Session) Close() {
	s.t.Helper()

	_, _ = s.tty.Write([]byte{0x04}) // Ctrl-D

	exited := make(chan error, 1)
	go func() { exited <- s.cmd.Wait() }()

	select {
	case err := <-exited:
		s.t.Logf("interactive session exited (err=%v)", err)
	case <-time.After(closeGrace):
		s.t.Logf("interactive session ignored EOT; terminating its process group")
		s.killGroup()
		<-exited
	}

	_ = s.tty.Close()
	<-s.done
	s.t.Logf("--- session transcript ---\n%s", s.Output())
}

// Mark returns a cursor into the output stream, for use with SeenSince.
//
// Expect searches the whole transcript, which suits a one-off startup dialog and
// nothing that can appear twice: the text stays in the buffer forever, so a second
// Expect matches the first occurrence and returns immediately.
func (s *Session) Mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.Len()
}

// SeenSince reports whether want has appeared since mark, without blocking.
//
// For a dialog the session may or may not meet. An unanswered modal swallows the
// next prompt: the text lands in the dialog and its Enter picks whatever was
// highlighted. Asking first turns a silent wrong turn into a handled one.
func (s *Session) SeenSince(mark int, want string) bool {
	raw := s.raw()
	if mark > len(raw) {
		mark = len(raw)
	}
	return strings.Contains(normalizeTUI(raw[mark:]), normalizeTUI(want))
}

// maxOptionMoves bounds the search for a list entry, so a dialog that never
// highlights what we asked for fails instead of pressing Down forever.
const maxOptionMoves = 8

// SelectOption moves a TUI list's selection onto want and confirms it.
//
// Always choose by text rather than pressing Enter on whatever is highlighted.
// What a TUI preselects is not a contract: Claude Code's folder-trust dialog opens
// on "No, exit", so a bare Enter quits the session and every later barrier times
// out.
//
// Selection is read from the marker a list draws before the highlighted entry, and
// the label must match EXACTLY: Copilot offers both "Yes" and "Yes, and remember
// this folder for future sessions". Claude and Copilot mark with "❯", Codex with
// "›", and a leading "1." is stripped before comparing.
func (s *Session) SelectOption(anchor, want string, timeout time.Duration) {
	s.t.Helper()

	s.Expect(anchor, timeout)

	for range maxOptionMoves {
		if s.selects(anchor, want) {
			s.Send("") // Enter, on the entry we asked for
			return
		}
		before := s.Mark()
		_, err := s.tty.Write([]byte(keyDown))
		require.NoError(s.t, err, "moving the selection")
		s.settleAfter(before)
	}
	s.t.Fatalf("never highlighted %q in the dialog matching %q after %d moves.\n"+
		"--- transcript (escape sequences stripped) ---\n%s",
		want, anchor, maxOptionMoves, s.Output())
}

// keyDown is the ANSI sequence for the Down arrow, which is how a TUI list moves
// its selection.
const keyDown = "\x1b[B"

// SelectionIs reports whether the NEWEST list anywhere in the transcript is
// sitting on want, without blocking and without being told which dialog drew it.
//
// SelectOption anchors on a line, so one dialog is never confused with another,
// and it assumes a dialog is already open. This is for a poll handler that must
// first decide WHETHER one is open at all — where the anchor is the thing being
// determined. A runtime that raises the same dialog for several different reasons
// mid-turn (Codex asks separately per file edit and per shell command) would
// otherwise need one anchor per reason, and the anchors interfere: everything
// after an earlier dialog's line includes the later dialog's marker.
//
// It self-clears. Once an answered dialog closes, the composer the runtime redraws
// carries the newest marker, and its label matches no entry.
func (s *Session) SelectionIs(want string) bool {
	label, target := s.selection(), collapseTUI(want)
	return label == target || strings.HasPrefix(label, target+" ")
}

// selection returns the label the newest marked line carries, or "" when nothing
// in the transcript is marked. Unanchored counterpart of highlighted.
func (s *Session) selection() string {
	text := stripEscapes(s.raw())
	marker, width := lastSelectionMarker(text)
	if marker < 0 {
		return ""
	}
	label := text[marker+width:]
	if end := strings.IndexAny(label, "\r\n"); end >= 0 {
		label = label[:end]
	}
	return entryLabel(collapseTUI(label))
}

// selects reports whether the list is currently sitting on want. Split out from
// SelectOption so the comparison is testable without a live session.
//
// The marked label either IS want, or it is want followed by a space and whatever
// the redraw put on the same physical line. Claude Code draws no entry numbers, so
// once a ConPTY concatenates its two entries the space is the only thing between
// them, and an exact match never comes: every frame of the API-key dialog on Windows
// reads "Yes No (recommended)".
//
// Requiring that space is what keeps the prefix hazard closed. Copilot offers both
// "Yes" and "Yes, and remember this folder for future sessions"; asking for "Yes"
// while the longer one is marked hits a comma rather than a space and is refused. So
// is a frame drawn with no separator at all, where "No,exitYes,Itrustthisfolder"
// matches neither entry — deliberate, since guessing there confirms whichever was
// drawn first.
func (s *Session) selects(anchor, want string) bool {
	label, target := s.highlighted(anchor), collapseTUI(want)
	return label == target || strings.HasPrefix(label, target+" ")
}

// highlighted returns the normalized label the list is sitting on, or "" when
// nothing after anchor is marked.
//
// It reads the marked LINE, not everything from the marker to the end of the
// buffer, so one option cannot be a prefix of another. The LAST marked line after
// the anchor is the current one: the dialog is redrawn on every keystroke and the
// transcript keeps every frame.
func (s *Session) highlighted(anchor string) string {
	text := stripEscapes(s.raw())

	// Anchor by line. Everything before it is another screen, such as the composer's
	// own "❯" prompt or a dialog already answered.
	offset, ok := lineOffsetAfter(text, anchor)
	if !ok {
		return ""
	}

	rest := text[offset:]
	marker, width := lastSelectionMarker(rest)
	if marker < 0 {
		return ""
	}

	// Cut at the end of the drawn line. A redraw positions the cursor instead of
	// emitting newlines, so two entries can share one physical line and the marked one
	// would come back as "No,exitYes,Itrustthisfolder", matching neither.
	label := rest[marker+width:]
	if end := strings.IndexAny(label, "\r\n"); end >= 0 {
		label = label[:end]
	}
	return entryLabel(collapseTUI(label))
}

// entryLabel trims a marked entry to its own text: the leading entry number
// removed, and anything from the NEXT entry number dropped.
//
// The second cut is what makes a cursor-positioned frame readable for the runtimes
// that number their entries. Codex and Copilot redraw a dialog by moving the cursor,
// so a frame arrives as "1. Yes  3. No (Esc)" on one physical line, and without the
// cut the marked label reads "YesNo(Esc)" and matches no entry. Every frame of those
// two dialogs is drawn that way, so waiting for a clean one never ends.
//
// It applies only when the label starts with a number, which is what says this
// runtime numbers its entries. Claude numbers nothing, so its concatenated frames
// still come back whole and still match nothing — deliberate, because with no
// delimiter, cutting at the first entry would confirm whichever was drawn first. That
// guard also keeps a version or a decimal inside an unnumbered label intact; see
// TestHighlightedHandlesAFrameDrawnWithoutNewlines.
//
// The following number is not assumed to be this one plus one. A partial redraw
// leaves gaps: the frame above is a real capture, and it jumps from 1 to 3.
func entryLabel(norm string) string {
	own := listOrdinal.FindStringIndex(norm)
	if own == nil {
		return norm
	}
	label := norm[own[1]:]
	if next := anyOrdinal.FindStringIndex(label); next != nil {
		label = label[:next[0]]
	}
	// Trimmed, because both cuts leave the space that separated the number from the
	// text, and a label starting with one matches nothing.
	return strings.TrimSpace(label)
}

// lineOffsetAfter returns the byte offset just past the start of the LAST line
// whose normalized form contains want.
func lineOffsetAfter(text, want string) (int, bool) {
	needle := normalizeTUI(want)
	offset, found := 0, false
	at := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if strings.Contains(normalizeTUI(line), needle) {
			offset, found = at, true
		}
		at += len(line)
	}
	return offset, found
}

// selectionMarkers are the glyphs the agents draw before the highlighted entry.
var selectionMarkers = []string{"❯", "›"}

// lastSelectionMarker returns the position and width of the final selection
// marker in text, or -1.
func lastSelectionMarker(text string) (int, int) {
	best, width := -1, 0
	for _, m := range selectionMarkers {
		if i := strings.LastIndex(text, m); i > best {
			best, width = i, len(m)
		}
	}
	return best, width
}

// listOrdinal matches a leading "1." style entry number, which Codex and Copilot
// draw and Claude does not.
var listOrdinal = regexp.MustCompile(`^[0-9]+\.`)

// anyOrdinal is listOrdinal unanchored, for finding where the NEXT entry starts in a
// frame drawn on one physical line. entryLabel consults it only once listOrdinal has
// matched, so an unnumbered label keeps a decimal of its own.
var anyOrdinal = regexp.MustCompile(`[0-9]+\.`)

// settleAfter waits briefly for the TUI to redraw after a keystroke, so the next
// read sees the moved selection rather than the previous frame.
func (s *Session) settleAfter(mark int) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.Mark() > mark {
			time.Sleep(pollInterval)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
