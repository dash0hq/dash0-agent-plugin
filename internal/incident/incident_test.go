// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package incident

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// write lays down one session's breadcrumb file in the exact format the hook
// registrations produce. Keep these lines in step with codex/hooks.json and
// install-codex.sh: the shell there is the only writer, and it cannot be unit
// tested from Go.
func write(t *testing.T, dataDir, session, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(Dir(dataDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(Dir(dataDir), session+".log"), []byte(content), 0o644))
}

// writeClaim lays down a claim the way Drain names one, standing in for a
// process that took the breadcrumbs and then died holding them.
func writeClaim(t *testing.T, dataDir, session, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(Dir(dataDir), 0o755))
	path := filepath.Join(Dir(dataDir), session+".log"+claimMark+"99999-1")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// claimsIn lists the claims left in a data root.
func claimsIn(t *testing.T, dataDir string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(Dir(dataDir), "*"+claimMark+"*"))
	require.NoError(t, err)
	return found
}

func TestDrainAggregatesRepeatsIntoOneIncident(t *testing.T) {
	dir := t.TempDir()
	// A broken session fires a hook per event, so the same failure repeats. One
	// record with a count says everything hundreds of records would.
	write(t, dir, "thr-1", "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/plugins/0.1.24/codex/codex-on-event.sh\tthr-1\n"+
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
	write(t, dir, "thr-1", "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n"+
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
	write(t, dir, "thr-1", "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	_, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()

	// The file is gone, and no claim is left behind to be mistaken for a
	// breadcrumb later. An empty directory is the whole point: the breadcrumbs a
	// drain consumed take no space until the next failure.
	entries, err := os.ReadDir(Dir(dir))
	require.NoError(t, err)
	assert.Empty(t, entries, "a drained breadcrumb must not be reported again")
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
	write(t, dir, "thr-1", "not a breadcrumb at all\n"+
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
	write(t, dir, "thr-1", "2026-08-25T14:15:16Z\thook_script_missing\n")

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
	write(t, dir, "thr-1", "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	// Drain renames before reading, so a hook that appends while we are working
	// creates a fresh file instead of writing into the one being consumed.
	got, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()
	require.Len(t, got, 1)

	write(t, dir, "thr-1", "2026-08-25T14:20:00Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")
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
	write(t, dir, "thr-1", "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	got, _, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)

	// The claim is still on disk, so nothing was lost — it is just not at the
	// name a fresh hook appends to.
	assert.Len(t, claimsIn(t, dir), 1, "an uncommitted claim must stay for a later invocation")
}

// A claim left by a process that died is adopted, so the breadcrumbs it was
// holding are eventually reported instead of sitting there forever.
func TestDrainAdoptsAnAbandonedClaim(t *testing.T) {
	dir := t.TempDir()
	abandoned := writeClaim(t, dir, "thr-1",
		"2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	// Older than the grace period: whoever held this is not coming back.
	old := time.Now().Add(-2 * orphanGrace)
	require.NoError(t, os.Chtimes(abandoned, old, old))

	got, commit, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1, "the abandoned breadcrumb should be picked up")
	assert.Equal(t, "hook_script_missing", got[0].Kind)

	commit()
	entries, err := os.ReadDir(Dir(dir))
	require.NoError(t, err)
	assert.Empty(t, entries, "the adopted claim is gone once reported")
}

// A claim taken from a file that had been sitting around must not look abandoned
// the instant it is claimed. rename(2) carries the old mtime over, and a
// breadcrumb is written when the hook breaks — which can be long before anything
// drains it — so without a fresh stamp a concurrent hook adopts a claim that is
// still being reported, and the incident goes out twice.
func TestDrainDoesNotAdoptAClaimItJustTook(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "thr-1", "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	// The breadcrumb has been there far longer than the grace period.
	old := time.Now().Add(-10 * orphanGrace)
	require.NoError(t, os.Chtimes(filepath.Join(Dir(dir), "thr-1.log"), old, old))

	// First drain claims it and is still working — no commit yet.
	got, _, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)

	// The claim must date from when it was claimed, not from when the breadcrumb
	// was written. This is the assertion that pins the fix: rename alone leaves
	// the old mtime and the grace period then measures the wrong thing.
	claims := claimsIn(t, dir)
	require.Len(t, claims, 1)
	fi, err := os.Stat(claims[0])
	require.NoError(t, err)
	assert.Less(t, time.Since(fi.ModTime()), orphanGrace,
		"a claim must not be adoptable the moment it is taken")

	// And a hook firing concurrently must not take it. Drain names each claim
	// uniquely, so this second call stands in for a second process.
	stolen, _, err := Drain(dir)
	require.NoError(t, err)
	assert.Empty(t, stolen, "a claim in flight must not be adopted out from under its owner")
}

// A claim young enough to belong to a live invocation is left alone, so two
// concurrent hooks do not report the same incident twice.
func TestDrainLeavesAFreshClaimAlone(t *testing.T) {
	dir := t.TempDir()
	fresh := writeClaim(t, dir, "thr-2",
		"2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	got, _, err := Drain(dir)
	require.NoError(t, err)
	assert.Empty(t, got, "another invocation is still working on that claim")

	_, err = os.Stat(fresh)
	assert.NoError(t, err, "and it must be left where it is")
}

// Breadcrumbs and an abandoned claim are reported together, aggregated.
func TestDrainCombinesTheCurrentFileWithAnAdoptedClaim(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "thr-1", "2026-08-25T14:20:00Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")

	abandoned := writeClaim(t, dir, "thr-1",
		"2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")
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

func TestDirIsInsideTheDataRoot(t *testing.T) {
	assert.Equal(t, filepath.Join("/state/codex", DirName), Dir("/state/codex"))
	assert.Empty(t, Dir(""), "no data root means no breadcrumbs to look for")
}

// One file per session is the point of the directory: a session that fills its
// own file cannot silence another session's breadcrumbs, and both are reported
// by whichever invocation drains first.
func TestDrainReportsEverySessionsFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "thr-1", "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")
	write(t, dir, "thr-2", "2026-08-25T14:15:17Z\thook_script_missing\tcodex\t/a.sh\tthr-2\n"+
		"2026-08-25T14:15:18Z\thook_script_missing\tcodex\t/a.sh\tthr-2\n")

	got, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()

	require.Len(t, got, 2, "a different session is a different incident")
	assert.Equal(t, "thr-1", got[0].SessionID)
	assert.Equal(t, 1, got[0].Count)
	assert.Equal(t, "thr-2", got[1].SessionID)
	assert.Equal(t, 2, got[1].Count)

	entries, err := os.ReadDir(Dir(dir))
	require.NoError(t, err)
	assert.Empty(t, entries, "both files are consumed, not just the first")
}

// The shell caps each session's file but cannot count files, so the bound on how
// many sessions may pile up is enforced here. Oldest goes first: a breadcrumb
// from a plugin that was broken months ago is worth less than today's.
func TestDrainEvictsTheOldestOverTheFileBound(t *testing.T) {
	dir := t.TempDir()
	prev := maxFiles
	t.Cleanup(func() { maxFiles = prev })
	maxFiles = 2

	for i, session := range []string{"old", "newer", "newest"} {
		write(t, dir, session, "2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\t"+session+"\n")
		stamp := time.Now().Add(time.Duration(i-10) * time.Hour)
		require.NoError(t, os.Chtimes(filepath.Join(Dir(dir), session+".log"), stamp, stamp))
	}

	got, commit, err := Drain(dir)
	require.NoError(t, err)
	commit()

	require.Len(t, got, 2)
	sessions := []string{got[0].SessionID, got[1].SessionID}
	assert.ElementsMatch(t, []string{"newer", "newest"}, sessions,
		"the oldest breadcrumb is dropped, not the newest")
}

// A file handed from one dead process to the next must not grow a claim suffix
// each time, or the name walks off toward the filesystem's limit.
func TestAdoptingAClaimDoesNotStackSuffixes(t *testing.T) {
	dir := t.TempDir()
	abandoned := writeClaim(t, dir, "thr-1",
		"2026-08-25T14:15:16Z\thook_script_missing\tcodex\t/a.sh\tthr-1\n")
	old := time.Now().Add(-2 * orphanGrace)
	require.NoError(t, os.Chtimes(abandoned, old, old))

	got, _, err := Drain(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)

	claims := claimsIn(t, dir)
	require.Len(t, claims, 1)
	assert.Equal(t, 1, strings.Count(filepath.Base(claims[0]), claimMark),
		"an adopted claim carries one claim marker, not one per hand-off")
}
