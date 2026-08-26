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
	"strings"
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

// Drain claims the breadcrumb file and returns what it held, aggregated.
//
// The file is renamed before it is read, so hooks running concurrently keep
// appending to a fresh file instead of writing lines into one being consumed.
// The claim is removed once parsed: the caller is expected to hand every
// Incident to something durable (a send that spools on failure), so the window
// where a crash loses a breadcrumb is the few microseconds in between.
func Drain(dataDir string) ([]Incident, error) {
	path := Path(dataDir)
	if path == "" {
		return nil, nil
	}

	claim := fmt.Sprintf("%s.claimed.%d", path, os.Getpid())
	if err := os.Rename(path, claim); err != nil {
		if os.IsNotExist(err) {
			return nil, nil // nothing to report, the common case
		}
		return nil, fmt.Errorf("incident: claiming %s: %w", path, err)
	}
	defer func() { _ = os.Remove(claim) }()

	f, err := os.Open(claim)
	if err != nil {
		return nil, fmt.Errorf("incident: reading %s: %w", claim, err)
	}
	defer func() { _ = f.Close() }()

	// Keys in insertion order, so the report reads in the order things broke.
	var order []string
	byKey := make(map[string]*Incident)

	scanner := bufio.NewScanner(f)
	for lines := 0; scanner.Scan() && lines < maxLines; lines++ {
		inc, ok := parse(scanner.Text())
		if !ok {
			continue
		}
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
	return out, nil
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
