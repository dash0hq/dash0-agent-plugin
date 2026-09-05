// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Selection matching against real captured frames. No build constraint and no
// stub REPL: these drive the parsing directly, so they also run on the Windows
// leg, which is the one whose dialogs they describe. The tests that need a fake
// REPL live in session_test.go, which POSIX-only stubs confine to !windows.

package agentterm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The approval dialog Codex raises on Windows before applying a file edit. Its
// entries are numbered and the first two share a "Yes, " prefix, so reading the
// marked one has to survive both — see approveCodexActions in
// test/e2e/codex_e2e_test.go, which confirms the first.
//
// The frames are a real capture from a windows-latest runner.
func TestHighlightedReadsCodexFileEditApproval(t *testing.T) {
	const anchor = "Apply proposed file edits"
	const want = "Yes, and don't ask again for these files (a)"

	// Frame 1: as drawn, marker on the first entry.
	s := &Session{t: t, done: make(chan struct{})}
	s.output.WriteString(
		"  Description: Apply proposed file edits\r\n" +
			"  Destination: C:\\Users\\runneradmin\\AppData\\Local\\Temp\\x\\004\\hello.txt\r\n" +
			"\r\n" +
			"› 1. Yes, proceed (y)\r\n" +
			"  2. Yes, and don't ask again for these files (a)\r\n" +
			"  3. No, and tell Codex what to do differently (esc)\r\n")
	assert.Equal(t, "Yes, proceed (y)", s.highlighted(anchor),
		"the entry number is stripped and the label keeps its (y) hint")
	assert.False(t, s.selects(anchor, want),
		"the wanted entry is not the marked one yet, so SelectOption must press Down")

	// Frame 2: redrawn after a Down, marker on the entry the canary asks for.
	s.output.WriteString(
		"  1. Yes, proceed (y)\r\n" +
			"› 2. Yes, and don't ask again for these files (a)\r\n" +
			"  3. No, and tell Codex what to do differently (esc)\r\n")
	assert.True(t, s.selects(anchor, want),
		"so SelectOption confirms here")

	// A bare "Yes" must not confirm this dialog: the marked label continues with a
	// comma rather than a space, which is the prefix guard in selects.
	assert.False(t, s.selects(anchor, "Yes"),
		"a prefix of the marked entry must not confirm it")
}

// The same list redrawn onto one physical line, which is what a ConPTY produces
// and what entryLabel's cut at the NEXT ordinal exists for.
func TestHighlightedReadsCodexApprovalDrawnOnOneLine(t *testing.T) {
	const anchor = "Apply proposed file edits"

	s := &Session{t: t, done: make(chan struct{})}
	s.output.WriteString(
		"  Description: Apply proposed file edits\r\n" +
			"  1. Yes, proceed (y) › 2. Yes, and don't ask again for these files (a) " +
			"3. No, and tell Codex what to do differently (esc)\r\n")
	assert.Equal(t, "Yes, and don't ask again for these files (a)", s.highlighted(anchor),
		"the marked entry is cut at the next entry number rather than running into it")
	assert.True(t, s.selects(anchor, "Yes, and don't ask again for these files (a)"),
		"a frame with no newline between the entries still resolves to the marked one")
}

// SelectionIs has to answer "is an approval dialog open right now", across the two
// different dialogs Codex raises mid-turn and the composer it redraws afterwards.
// Every frame below is a real capture from a windows-latest runner.
func TestSelectionIsTracksTheNewestDialog(t *testing.T) {
	s := &Session{t: t, done: make(chan struct{})}

	// The composer, before anything is asked.
	s.output.WriteString("› Ask Codex to do anything\r\n  gpt-5.6-sol default · ~/x/004\r\n")
	assert.False(t, s.SelectionIs(approvalEntry),
		"an idle composer is not a dialog")

	// The file-edit dialog opens; its marker is newer than the composer's.
	s.output.WriteString(
		"  Description: Apply proposed file edits\r\n" +
			"› 1. Yes, proceed (y)\r\n" +
			"  2. Yes, and don't ask again for these files (a)\r\n")
	assert.True(t, s.SelectionIs(approvalEntry),
		"the newest marker is the dialog's, so it reads as open")

	// Answered: Codex redraws the composer, whose marker is newer again.
	s.output.WriteString("› Ask Codex to do anything\r\n")
	assert.False(t, s.SelectionIs(approvalEntry),
		"the handler must re-arm once the dialog closes, with no mark to keep")

	// A DIFFERENT dialog later in the same turn, for a shell command.
	s.output.WriteString(
		"  Would you like to run the following command?\r\n" +
			"  $ cat hello.txt\r\n" +
			"› 1. Yes, proceed (y)\r\n" +
			"  2. Yes, and don't ask again for commands that start with `cat hello.txt` (p)\r\n")
	assert.True(t, s.SelectionIs(approvalEntry),
		"the second dialog is answered by the same entry, with no anchor to pick")
}

// The entry carries trailing text when a redraw puts the spinner on its line, and
// a bare prefix must still not confirm it.
func TestSelectionIsToleratesTrailingRedraw(t *testing.T) {
	s := &Session{t: t, done: make(chan struct{})}
	s.output.WriteString("› 1. Yes, proceed (y)      • Working (6s • esc to interrupt)\r\n")
	assert.True(t, s.SelectionIs(approvalEntry),
		"a spinner sharing the physical line must not hide the entry")

	other := &Session{t: t, done: make(chan struct{})}
	other.output.WriteString("› 1. Yes, and don't ask again for these files (a)\r\n")
	assert.False(t, other.SelectionIs(approvalEntry),
		"a different entry of the same dialog must not be mistaken for it")
}

// approvalEntry mirrors codexApprovalEntry in test/e2e/codex_e2e_test.go, which is
// e2e-tagged and so cannot be imported here.
const approvalEntry = "Yes, proceed (y)"
