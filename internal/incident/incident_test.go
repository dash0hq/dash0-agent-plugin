// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package incident

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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

	got, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()
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

	got, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()
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

	_, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()

	_, err = os.Stat(Path(dir))
	assert.True(t, os.IsNotExist(err), "a drained breadcrumb must not be reported again")

	// And no claim file is left behind to be mistaken for a breadcrumb later.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestDrainWithNothingToReportIsQuiet(t *testing.T) {
	got, _, err := Drain(t.TempDir())
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

	got, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()
	require.Len(t, got, 1)
	assert.Equal(t, "hook_script_missing", got[0].Kind)
}

func TestDrainToleratesShortLines(t *testing.T) {
	dir := t.TempDir()
	// Timestamp and kind are the minimum; the rest is best-effort context.
	write(t, dir, "2026-08-25T14:15:16Z\thook_script_missing\n")

	got, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()
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
	got, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()
	require.Len(t, got, 1)

	write(t, dir, "2026-08-25T14:20:00Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")
	got, commit, err = Drain(dir)
	require.NoError(t, err)
	commit()
	require.Len(t, got, 1)
	assert.Equal(t, "2026-08-25T14:20:00Z", got[0].Last.Format("2006-01-02T15:04:05Z"))
}

// Without a commit the breadcrumbs survive. Reporting an incident can spend the
// OTLP client's whole timeout budget before the payload reaches the spool, and a
// hook killed in that window must not take the only evidence with it.
func TestDrainWithoutCommitKeepsTheBreadcrumbs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	got, _, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)

	// The claim is still on disk, so nothing was lost — it is just not at the
	// name a fresh hook appends to.
	claims, err := filepath.Glob(Path(dir) + ".claimed.*")
	require.NoError(t, err)
	assert.Len(t, claims, 1, "an uncommitted claim must stay for a later invocation")
}

// A claim left by a process that died is adopted, so the breadcrumbs it was
// holding are eventually reported instead of sitting there forever.
func TestDrainAdoptsAnAbandonedClaim(t *testing.T) {
	dir := t.TempDir()
	abandoned := Path(dir) + ".claimed.99999"
	require.NoError(t, os.WriteFile(abandoned,
		[]byte("2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n"), 0o644))

	// Older than the grace period: whoever held this is not coming back.
	old := time.Now().Add(-2 * orphanGrace)
	require.NoError(t, os.Chtimes(abandoned, old, old))

	got, commit, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1, "the abandoned breadcrumb should be picked up")
	assert.Equal(t, "hook_script_missing", got[0].Kind)

	commit()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the adopted claim is gone once reported")
}

// A claim young enough to belong to a live invocation is left alone, so two
// concurrent hooks do not report the same incident twice.
func TestDrainLeavesAFreshClaimAlone(t *testing.T) {
	dir := t.TempDir()
	fresh := Path(dir) + ".claimed.99999"
	require.NoError(t, os.WriteFile(fresh,
		[]byte("2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n"), 0o644))

	got, _, err := Drain(dir)
	require.NoError(t, err)
	assert.Empty(t, got, "another invocation is still working on that claim")

	_, err = os.Stat(fresh)
	assert.NoError(t, err, "and it must be left where it is")
}

// Breadcrumbs and an abandoned claim are reported together, aggregated.
func TestDrainCombinesTheCurrentFileWithAnAdoptedClaim(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "2026-08-25T14:20:00Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	abandoned := Path(dir) + ".claimed.99999"
	require.NoError(t, os.WriteFile(abandoned,
		[]byte("2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n"), 0o644))
	old := time.Now().Add(-2 * orphanGrace)
	require.NoError(t, os.Chtimes(abandoned, old, old))

	got, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()

	require.Len(t, got, 1, "the same failure from both files is one incident")
	assert.Equal(t, 2, got[0].Count)
	assert.Equal(t, "2026-08-25T14:15:16Z", got[0].First.Format("2006-01-02T15:04:05Z"))
	assert.Equal(t, "2026-08-25T14:20:00Z", got[0].Last.Format("2006-01-02T15:04:05Z"))
}

func TestPathIsInsideTheDataRoot(t *testing.T) {
	assert.Equal(t, filepath.Join("/state/codex", FileName), Path("/state/codex"))
	assert.Empty(t, Path(""), "no data root means no breadcrumb file to look for")
}
