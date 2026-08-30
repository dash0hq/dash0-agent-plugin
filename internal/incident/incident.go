// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package incident reads the breadcrumbs the hook registrations leave when they
// cannot run the plugin at all.
//
// The registered hook command is a shell string that calls the bootstrap script.
// When that script is missing — a `codex plugin` update deletes the version-scoped
// plugin root a live session is still pointing at — no Go code of ours runs, so
// nothing can report the failure over OTLP at the time. The command instead
// appends one line to a file in the state directory, which survives the update.
// This package turns those lines into something the next working invocation can
// ship, which is the only way we ever learn the plugin was mute.
//
// Breadcrumbs go into a directory, one file per session, the same shape the
// telemetry spool uses: a writer owns its own file, and a drain deletes what it
// consumed instead of growing a log nobody truncates. One shared file would let a
// single broken session hit the shell's size cap and silence the breadcrumbs of
// every other session on the machine.
//
// The format is fixed by the shell that writes it (see codex/hooks.json and
// install-codex.sh) and is tab separated:
//
//	<RFC3339 UTC>\t<kind>\t<harness>\t<detail>\t<session id>
package incident

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// DirName is the breadcrumb directory inside a data root.
const DirName = "incidents"

// maxLines bounds the work one invocation does. A session that fires hundreds of
// hooks while broken writes a line each time; the shell caps each file's size and
// this caps the parse.
const maxLines = 5000

// maxFiles bounds the directory. The shell caps a session's own file, but it
// cannot count files without a subshell per hook, so the bound on how many
// sessions may pile up lives here. Oldest goes first: a stale breadcrumb from a
// plugin that was broken months ago is worth less than today's.
//
// Nothing sweeps while the plugin stays broken, because nothing of ours runs then.
// That is bounded in practice — one small file per broken session — and the first
// working invocation cleans it up.
//
// A var, not a const, so a test can shrink it instead of writing 256 files.
var maxFiles = 256

// Incident is one kind of failure, aggregated over however many times it
// happened. Reporting each line separately would put hundreds of identical
// records in the user's dataset and say nothing more than the count does.
type Incident struct {
	Kind      string
	Harness   string
	Detail    string
	SessionID string
	Count     int
	First     time.Time
	Last      time.Time
}

// Dir returns the breadcrumb directory for a data root.
func Dir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, DirName)
}

// orphanGrace is how long a claim must sit untouched before another invocation
// adopts it. A live drain finishes in seconds — one report, with the OTLP
// client's own timeouts as the ceiling — so anything older than this belongs to a
// process that died holding it.
const orphanGrace = 5 * time.Minute

// Drain claims every breadcrumb file and returns what they held, aggregated,
// together with a commit that deletes them.
//
// Each file is renamed before it is read, so a session still writing keeps
// appending to a fresh file instead of into one being consumed. A drain can take
// the last few lines of a session that is broken right now; the report aggregates
// by kind, so the cost is a count that is short by one or two, not a lost
// incident.
//
// The claims are deliberately NOT removed here. Reporting an incident can take as
// long as the OTLP client's timeouts allow before the payload reaches the spool,
// and a hook killed inside that window would take the only evidence that the
// plugin was mute with it. So the caller commits once every incident is durable,
// and a claim left behind by a process that died is adopted by a later one.
// Reporting an incident twice is a far better failure than losing it.
func Drain(dataDir string) (incidents []Incident, commit func(), err error) {
	dir := Dir(dataDir)
	if dir == "" {
		return nil, func() {}, nil
	}

	claims, err := claim(dir)
	if err != nil {
		return nil, func() {}, err
	}
	commit = func() {
		for _, c := range claims {
			_ = os.Remove(c)
		}
	}
	if len(claims) == 0 {
		return nil, commit, nil // nothing to report, the common case
	}

	// Keys in insertion order, so the report reads in the order things broke.
	var order []string
	byKey := make(map[string]*Incident)

	for _, inc := range readAll(claims) {
		key := strings.Join([]string{inc.Kind, inc.Harness, inc.Detail, inc.SessionID}, "\x00")
		existing, seen := byKey[key]
		if !seen {
			copied := inc
			byKey[key] = &copied
			order = append(order, key)
			continue
		}
		existing.Count++
		if inc.Last.After(existing.Last) {
			existing.Last = inc.Last
		}
		if inc.First.Before(existing.First) {
			existing.First = inc.First
		}
	}

	out := make([]Incident, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	return out, commit, nil
}

// claimSeq keeps one process's claim names distinct, so two drains in the same
// process behave like two processes rather than mistaking each other's claims
// for their own.
var claimSeq atomic.Uint64

// claimMark separates a breadcrumb file's name from the claim that owns it.
const claimMark = ".claimed."

// claim takes ownership of every breadcrumb file, and of any claim a dead process
// left behind, returning the paths this invocation now owns.
//
// Ownership is established by rename, which is atomic within a directory: two
// invocations racing for the same file means exactly one of them gets it.
func claim(dir string) ([]string, error) {
	names, err := entries(dir)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil
	}
	names = evict(dir, names)

	seq := func(path string) string {
		return fmt.Sprintf("%s%s%d-%d", path, claimMark, os.Getpid(), claimSeq.Add(1))
	}

	var claims []string
	for _, name := range names {
		path := filepath.Join(dir, name)
		if !strings.Contains(name, claimMark) {
			mine := seq(path)
			if err := os.Rename(path, mine); err != nil {
				continue // another invocation got there first
			}
			claims = append(claims, touch(mine))
			continue
		}

		// A claim. Adopt it only if it is old: a fresh one belongs to an
		// invocation that is still reporting it.
		fi, err := os.Stat(path)
		if err != nil || time.Since(fi.ModTime()) < orphanGrace {
			continue
		}
		// Strip the previous claim before adding ours, so a file passed between
		// dead processes does not grow a suffix per hand-off. Cut on the name, not
		// the path: the data root is out of our control and may contain anything.
		adopted := seq(filepath.Join(dir, name[:strings.Index(name, claimMark)]))
		if err := os.Rename(path, adopted); err != nil {
			continue
		}
		claims = append(claims, touch(adopted))
	}
	return claims, nil
}

// entries lists the breadcrumb directory in name order, which is only there to
// make a drain deterministic — a session id says nothing about time.
func entries(dir string) ([]string, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // the common case: nothing ever broke
		}
		return nil, fmt.Errorf("incident: reading %s: %w", dir, err)
	}
	names := make([]string, 0, len(des))
	for _, de := range des {
		if !de.IsDir() {
			names = append(names, de.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}

// evict drops the oldest files when the directory is over its bound, and returns
// what is left to claim. Sorting is by name, which is a session id and says
// nothing about age, so mtime decides.
//
// A failure to remove is ignored. Being over the bound is not a reason to report
// nothing.
func evict(dir string, names []string) []string {
	if len(names) <= maxFiles {
		return names
	}
	byAge := slices.Clone(names)
	mtime := make(map[string]time.Time, len(byAge))
	for _, name := range byAge {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
			mtime[name] = fi.ModTime()
		}
	}
	slices.SortStableFunc(byAge, func(a, b string) int { return mtime[a].Compare(mtime[b]) })

	dropped := make(map[string]bool, len(byAge)-maxFiles)
	for _, name := range byAge[:len(byAge)-maxFiles] {
		_ = os.Remove(filepath.Join(dir, name))
		dropped[name] = true
	}
	return slices.DeleteFunc(names, func(name string) bool { return dropped[name] })
}

// touch stamps a claim with the time it was claimed, and returns it unchanged.
//
// The grace period is meant to measure how long a claim has gone unattended, but
// rename(2) carries the original mtime over — and a breadcrumb file was written
// when the hook broke, which can be hours before anything drains it. Without this
// a claim is born already looking abandoned, and a concurrent hook adopts it out
// from under the invocation that is still reporting it. Codex runs up to eight
// hooks at a time, so that is a routine race, not a corner case.
//
// A failure here is not worth reporting: the claim is ours either way, and the
// cost is a duplicate report rather than a lost one.
func touch(claim string) string {
	now := time.Now()
	_ = os.Chtimes(claim, now, now)
	return claim
}

// readAll parses every claimed file. Lines within a file are already in the order
// they happened, and aggregation takes the earliest first and the latest last, so
// the sort is only here to make the report's own order deterministic.
func readAll(claims []string) []Incident {
	slices.Sort(claims)

	var out []Incident
	lines := 0
	for _, c := range claims {
		f, err := os.Open(c)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() && lines < maxLines {
			lines++
			if inc, ok := parse(scanner.Text()); ok {
				out = append(out, inc)
			}
		}
		_ = f.Close()
	}
	return out
}

// parse reads one breadcrumb line. Anything malformed is skipped rather than
// failing the batch: the writer is a shell one-liner running in a broken
// environment, so a truncated last line is expected, not exceptional.
func parse(line string) (Incident, bool) {
	fields := strings.Split(strings.TrimSpace(line), "\t")
	if len(fields) < 2 {
		return Incident{}, false
	}
	ts, err := time.Parse(time.RFC3339, fields[0])
	if err != nil {
		return Incident{}, false
	}
	inc := Incident{
		Kind:  fields[1],
		Count: 1,
		First: ts.UTC(),
		Last:  ts.UTC(),
	}
	if len(fields) > 2 {
		inc.Harness = fields[2]
	}
	if len(fields) > 3 {
		inc.Detail = fields[3]
	}
	if len(fields) > 4 {
		inc.SessionID = fields[4]
	}
	return inc, true
}
