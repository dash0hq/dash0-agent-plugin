// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package spool keeps OTLP payloads that could not be sent, so a later
// invocation can send them instead of the data being lost.
//
// Every hook invocation is a fresh process with no retry budget beyond its own
// lifetime: when the endpoint is unreachable, the payload used to be dropped and
// the session's traces and token counts simply never arrived. Nothing said so —
// the cost analysis was quietly short. A hook that runs later, from a machine
// that has its network back, is the only thing in a position to send them.
//
// The spool is deliberately dumb: one file per payload, ordered by name, drained
// oldest-first. It is bounded in both count and bytes because it lives in the
// user's state directory and nothing guarantees anyone ever comes back to drain
// it; when it is full the oldest payload goes, on the grounds that the newest
// telemetry is the most useful.
package spool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// DirName is the spool's directory inside a data root.
	DirName = "spool"

	// MaxFiles and MaxBytes bound the spool. A payload that would push it past
	// either limit evicts the oldest entries first.
	MaxFiles = 512
	MaxBytes = 32 << 20 // 32 MiB
)

// The bounds the code actually reads, so tests can shrink them instead of
// writing 32 MiB to prove eviction works.
var (
	maxFiles = MaxFiles
	maxBytes = MaxBytes
)

// suffixes maps the OTLP path a payload belongs to onto the filename suffix that
// records it. A spooled file has to remember where to POST itself, and the
// suffix keeps that out of the file's contents so draining needs no parsing.
var suffixes = map[string]string{
	"/v1/traces":  "traces",
	"/v1/logs":    "logs",
	"/v1/metrics": "metrics",
}

// counter disambiguates two payloads spooled in the same nanosecond by one
// process. Concurrent hooks are separate processes, so the pid covers those.
var counter atomic.Uint64

// Dir returns the spool directory for a data root.
func Dir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, DirName)
}

// Append stores a payload that could not be sent. otlpPath is the OTLP path it
// was destined for. A payload larger than the whole budget is refused rather
// than allowed to evict everything else.
func Append(dir, otlpPath string, payload []byte) error {
	suffix, ok := suffixes[otlpPath]
	if !ok {
		return fmt.Errorf("spool: unknown OTLP path %q", otlpPath)
	}
	if dir == "" {
		return errors.New("spool: no directory configured")
	}
	if len(payload) > maxBytes {
		return fmt.Errorf("spool: payload of %d bytes exceeds the %d byte budget", len(payload), maxBytes)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("spool: creating %s: %w", dir, err)
	}

	if err := evictFor(dir, len(payload)); err != nil {
		return err
	}

	// Fixed-width nanoseconds so a lexical sort is a chronological sort.
	name := fmt.Sprintf("%019d-%d-%d.%s", time.Now().UnixNano(), os.Getpid(), counter.Add(1), suffix)
	final := filepath.Join(dir, name)

	// Write to a temp and rename: a concurrent Drain must never read a partial
	// payload, and rename(2) within a directory is atomic.
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return fmt.Errorf("spool: writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("spool: renaming into %s: %w", final, err)
	}
	return nil
}

// Drain sends spooled payloads oldest-first through send, removing each one that
// is accepted. It stops at the first failure — the endpoint being unreachable is
// the normal reason a payload is here, and there is no point walking the whole
// spool to learn that twice — and at limit payloads or the deadline, whichever
// comes first, so a large backlog cannot stall the agent's hook.
//
// It returns the number sent. A send failure is not an error: the payload stays
// spooled for the next invocation.
func Drain(dir string, limit int, deadline time.Time, send func(otlpPath string, payload []byte) error) (int, error) {
	names, err := entries(dir)
	if err != nil || len(names) == 0 {
		return 0, err
	}

	sent := 0
	for _, name := range names {
		if sent >= limit || !time.Now().Before(deadline) {
			break
		}
		otlpPath, ok := pathFor(name)
		if !ok {
			// Not ours, or from a newer layout we do not understand. Leave it.
			continue
		}
		full := filepath.Join(dir, name)
		payload, err := os.ReadFile(full)
		if err != nil {
			// Another invocation drained it between the listing and here.
			continue
		}
		if err := send(otlpPath, payload); err != nil {
			break
		}
		_ = os.Remove(full)
		sent++
	}
	return sent, nil
}

// Len reports how many payloads are waiting.
func Len(dir string) int {
	names, err := entries(dir)
	if err != nil {
		return 0
	}
	return len(names)
}

// entries lists spooled payloads in chronological order, skipping temp files.
func entries(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("spool: reading %s: %w", dir, err)
	}
	names := make([]string, 0, len(des))
	for _, de := range des {
		if de.IsDir() || strings.HasSuffix(de.Name(), ".tmp") {
			continue
		}
		names = append(names, de.Name())
	}
	slices.Sort(names)
	return names, nil
}

// pathFor recovers the OTLP path a spooled file was destined for.
func pathFor(name string) (string, bool) {
	dot := strings.LastIndex(name, ".")
	if dot < 0 {
		return "", false
	}
	for otlpPath, suffix := range suffixes {
		if name[dot+1:] == suffix {
			return otlpPath, true
		}
	}
	return "", false
}

// evictFor removes the oldest payloads until incoming bytes fit within both
// bounds. Newer telemetry is worth more than older, so age decides.
func evictFor(dir string, incoming int) error {
	names, err := entries(dir)
	if err != nil {
		return err
	}

	total := 0
	sizes := make(map[string]int, len(names))
	for _, name := range names {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		sizes[name] = int(fi.Size())
		total += sizes[name]
	}

	// Index rather than reslice: the payload about to be written needs a free
	// slot, so evict while the remainder would still be at or over the limit.
	for i := range names {
		remaining := len(names) - i
		if remaining < maxFiles && total+incoming <= maxBytes {
			break
		}
		name := names[i]
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("spool: evicting %s: %w", name, err)
		}
		total -= sizes[name]
	}
	return nil
}
