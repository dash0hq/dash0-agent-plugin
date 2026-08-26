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

// FileName is the breadcrumb log inside a data root.
const FileName = "incidents.log"

// maxLines bounds the work one invocation does. A session that fires hundreds of
// hooks while broken writes a line each time; the shell caps the file's size and
// this caps the parse.
const maxLines = 5000

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

// Path returns the breadcrumb file for a data root.
func Path(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, FileName)
}

// orphanGrace is how long a claim must sit untouched before another invocation
// adopts it. A live drain finishes in seconds — one report, with the OTLP
// client's own timeouts as the ceiling — so anything older than this belongs to a
// process that died holding it.
const orphanGrace = 5 * time.Minute

// Drain claims the breadcrumbs and returns what they held, aggregated, together
// with a commit that discards them.
//
// The file is renamed before it is read, so hooks running concurrently keep
// appending to a fresh file instead of writing lines into one being consumed.
//
// The claim is deliberately NOT removed here. Reporting an incident can take as
// long as the OTLP client's timeouts allow before the payload reaches the spool,
// and a hook killed inside that window would take the only evidence that the
// plugin was mute with it. So the caller commits once every incident is durable,
// and a claim left behind by a process that died is adopted by a later one.
// Reporting an incident twice is a far better failure than losing it.
func Drain(dataDir string) (incidents []Incident, commit func(), err error) {
	path := Path(dataDir)
	if path == "" {
		return nil, func() {}, nil
	}

	claims, err := claim(path)
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

// claim takes ownership of the breadcrumb file and of any claim a dead process
// left behind, returning the paths this invocation now owns.
//
// Ownership is established by rename, which is atomic within a directory: two
// invocations racing for the same file means exactly one of them gets it.
func claim(path string) ([]string, error) {
	name := func() string {
		return fmt.Sprintf("%s.claimed.%d-%d", path, os.Getpid(), claimSeq.Add(1))
	}

	var claims []string
	mine := name()
	switch err := os.Rename(path, mine); {
	case err == nil:
		claims = append(claims, touch(mine))
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("incident: claiming %s: %w", path, err)
	}

	// Adopt claims abandoned by processes that died mid-report. Only old ones:
	// a fresh claim belongs to an invocation that is still working on it.
	abandoned, err := filepath.Glob(path + ".claimed.*")
	if err != nil {
		return claims, nil // a bad pattern is not possible here, but never fail the drain
	}
	for _, other := range abandoned {
		if other == mine {
			continue
		}
		fi, err := os.Stat(other)
		if err != nil || time.Since(fi.ModTime()) < orphanGrace {
			continue
		}
		adopted := name()
		if err := os.Rename(other, adopted); err != nil {
			continue // another invocation adopted it first
		}
		claims = append(claims, touch(adopted))
	}
	return claims, nil
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

// readAll parses every claimed file, oldest name first so repeated failures
// aggregate in the order they happened.
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
