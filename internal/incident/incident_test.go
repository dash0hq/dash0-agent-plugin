// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package incident

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// write lays down a breadcrumb file in the exact format the hook registrations
// produce. Keep these lines in step with codex/hooks.json and install-codex.sh:
// the shell there is the only writer, and it cannot be unit tested from Go.
func write(t *testing.T, dataDir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(Path(dataDir), []byte(content), 0o644))
}

func TestDrainAggregatesRepeatsIntoOneIncident(t *testing.T) {
	dir := t.TempDir()
	// A broken session fires a hook per event, so the same failure repeats. One
	// record with a count says everything hundreds of records would.
	write(t, dir, "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/plugins/0.1.24/codex/codex-on-event.sh\tthr-1\n"+
		"2026-08-25T14:15:20Z\thook_script_missing\tcodex\t/plugins/0.1.24/codex/codex-on-event.sh\tthr-1\n"+
		"2026-08-25T14:16:00Z\thook_script_missing\tcodex\t/plugins/0.1.24/codex/codex-on-event.sh\tthr-1\n")

	got, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, "hook_script_missing", got[0].Kind)
	assert.Equal(t, "codex", got[0].Harness)
	assert.Equal(t, "/plugins/0.1.24/codex/codex-on-event.sh", got[0].Detail)
	assert.Equal(t, "thr-1", got[0].SessionID)
	assert.Equal(t, 3, got[0].Count)
	assert.Equal(t, "2026-08-25T14:15:16Z", got[0].First.Format("2006-01-02T15:04:05Z"))
	assert.Equal(t, "2026-08-25T14:16:00Z", got[0].Last.Format("2006-01-02T15:04:05Z"))
}

func TestDrainKeepsDistinctFailuresApart(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n"+
		"2026-08-25T14:15:17Z\thook_script_missing\tcodex\t/b.sh\tthr-1\n"+
		"2026-08-25T14:15:18Z\thook_script_missing\tcodex\t/a.sh\tthr-2\n")

	got, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 3, "a different path or session is a different incident")

	// Insertion order, so the report reads in the order things broke.
	assert.Equal(t, "/a.sh", got[0].Detail)
	assert.Equal(t, "thr-1", got[0].SessionID)
	assert.Equal(t, "/b.sh", got[1].Detail)
	assert.Equal(t, "/a.sh", got[2].Detail)
	assert.Equal(t, "thr-2", got[2].SessionID)
}

func TestDrainConsumesTheFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	_, err := Drain(dir)
	require.NoError(t, err)

	_, err = os.Stat(Path(dir))
	assert.True(t, os.IsNotExist(err), "a drained breadcrumb must not be reported again")

	// And no claim file is left behind to be mistaken for a breadcrumb later.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDrainWithNothingToReportIsQuiet(t *testing.T) {
	got, err := Drain(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestDrainSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	// The writer is a shell one-liner running in a broken environment, so a
	// truncated or garbled line is expected. It must not cost the good lines.
	write(t, dir, "not a breadcrumb at all\n"+
		"2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n"+
		"\n"+
		"2026-08-25T14:15:17\tbad_timestamp\tcodex\t/a.sh\tthr-1\n"+
		"2026-08-25T14:15:18Z\n")

	got, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "hook_script_missing", got[0].Kind)
}

func TestDrainToleratesShortLines(t *testing.T) {
	dir := t.TempDir()
	// Timestamp and kind are the minimum; the rest is best-effort context.
	write(t, dir, "2026-08-25T14:15:16Z\thook_script_missing\n")

	got, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "hook_script_missing", got[0].Kind)
	assert.Empty(t, got[0].Harness)
	assert.Empty(t, got[0].Detail)
}

func TestDrainDoesNotSwallowConcurrentBreadcrumbs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	// Drain renames before reading, so a hook that appends while we are working
	// creates a fresh file instead of writing into the one being consumed.
	got, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)

	write(t, dir, "2026-08-25T14:20:00Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")
	got, err = Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "2026-08-25T14:20:00Z", got[0].Last.Format("2006-01-02T15:04:05Z"))
}

func TestPathIsInsideTheDataRoot(t *testing.T) {
	assert.Equal(t, filepath.Join("/state/codex", FileName), Path("/state/codex"))
	assert.Empty(t, Path(""), "no data root means no breadcrumb file to look for")
}
