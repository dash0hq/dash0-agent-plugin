// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package spool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// far is a deadline no test will reach, for cases not about the time budget.
func far() time.Time { return time.Now().Add(time.Hour) }

type call struct {
	path    string
	payload string
}

// collect returns a send func that records what it was given.
func collect(calls *[]call) func(string, []byte) error {
	return func(path string, payload []byte) error {
		*calls = append(*calls, call{path, string(payload)})
		return nil
	}
}

func TestAppendAndDrainRoundTripsPathAndPayload(t *testing.T) {
	dir := Dir(t.TempDir())

	require.NoError(t, Append(dir, "/v1/traces", []byte(`{"resourceSpans":[1]}`)))
	require.NoError(t, Append(dir, "/v1/logs", []byte(`{"resourceLogs":[2]}`)))
	require.NoError(t, Append(dir, "/v1/metrics", []byte(`{"resourceMetrics":[3]}`)))

	var calls []call
	sent, err := Drain(dir, 10, far(), collect(&calls))
	require.NoError(t, err)

	// Oldest first: the order data happened in is the order it should arrive in.
	assert.Equal(t, 3, sent)
	assert.Equal(t, []call{
		{"/v1/traces", `{"resourceSpans":[1]}`},
		{"/v1/logs", `{"resourceLogs":[2]}`},
		{"/v1/metrics", `{"resourceMetrics":[3]}`},
	}, calls)
	assert.Zero(t, Len(dir), "a drained payload must not be sent twice")
}

func TestDrainStopsAtTheFirstFailureAndKeepsTheRest(t *testing.T) {
	dir := Dir(t.TempDir())
	for _, body := range []string{"one", "two", "three"} {
		require.NoError(t, Append(dir, "/v1/traces", []byte(body)))
	}

	// The endpoint being unreachable is why payloads are here at all, so walking
	// the rest of the spool would only learn the same thing again.
	attempts := 0
	sent, err := Drain(dir, 10, far(), func(string, []byte) error {
		attempts++
		if attempts == 2 {
			return errors.New("connection refused")
		}
		return nil
	})
	require.NoError(t, err, "a send failure is the expected case, not an error")
	assert.Equal(t, 1, sent)
	assert.Equal(t, 2, attempts)
	assert.Equal(t, 2, Len(dir), "the failed payload and everything after it must stay")

	// The next invocation picks up exactly where this one stopped.
	var calls []call
	sent, err = Drain(dir, 10, far(), collect(&calls))
	require.NoError(t, err)
	assert.Equal(t, 2, sent)
	assert.Equal(t, []call{{"/v1/traces", "two"}, {"/v1/traces", "three"}}, calls)
}

func TestDrainHonoursTheBatchLimit(t *testing.T) {
	dir := Dir(t.TempDir())
	for range 5 {
		require.NoError(t, Append(dir, "/v1/traces", []byte("x")))
	}

	sent, err := Drain(dir, 2, far(), func(string, []byte) error { return nil })
	require.NoError(t, err)
	assert.Equal(t, 2, sent)
	assert.Equal(t, 3, Len(dir))
}

func TestDrainHonoursTheDeadline(t *testing.T) {
	dir := Dir(t.TempDir())
	for range 5 {
		require.NoError(t, Append(dir, "/v1/traces", []byte("x")))
	}

	// A deadline already in the past means the hook is out of budget: send
	// nothing rather than adding latency to the user's session.
	sent, err := Drain(dir, 10, time.Now().Add(-time.Second), func(string, []byte) error {
		t.Error("send must not be called once the budget is spent")
		return nil
	})
	require.NoError(t, err)
	assert.Zero(t, sent)
	assert.Equal(t, 5, Len(dir))
}

func TestDrainOfAnEmptyOrMissingSpoolIsQuiet(t *testing.T) {
	sent, err := Drain(Dir(t.TempDir()), 10, far(), func(string, []byte) error {
		t.Error("nothing to send")
		return nil
	})
	require.NoError(t, err)
	assert.Zero(t, sent)
	assert.Zero(t, Len(Dir(t.TempDir())))
}

func TestAppendEvictsTheOldestWhenFull(t *testing.T) {
	t.Cleanup(func() { maxFiles = MaxFiles })
	maxFiles = 3

	dir := Dir(t.TempDir())
	for _, body := range []string{"1", "2", "3", "4", "5"} {
		require.NoError(t, Append(dir, "/v1/traces", []byte(body)))
	}

	var calls []call
	_, err := Drain(dir, 10, far(), collect(&calls))
	require.NoError(t, err)

	// Newer telemetry is worth more than older, so age decides what goes.
	assert.Len(t, calls, 3)
	assert.Equal(t, []call{
		{"/v1/traces", "3"}, {"/v1/traces", "4"}, {"/v1/traces", "5"},
	}, calls)
}

func TestAppendEvictsToStayUnderTheByteBudget(t *testing.T) {
	t.Cleanup(func() { maxBytes = MaxBytes })
	maxBytes = 30

	dir := Dir(t.TempDir())
	for _, body := range []string{"aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc", "dddddddddd"} {
		require.NoError(t, Append(dir, "/v1/traces", []byte(body)))
	}

	var calls []call
	_, err := Drain(dir, 10, far(), collect(&calls))
	require.NoError(t, err)

	// Three 10-byte payloads fit in the 30-byte budget, so the fourth evicts
	// exactly one: eviction frees what the incoming payload needs, no more.
	assert.Equal(t, []call{
		{"/v1/traces", "bbbbbbbbbb"}, {"/v1/traces", "cccccccccc"}, {"/v1/traces", "dddddddddd"},
	}, calls)
}

func TestAppendRefusesAPayloadBiggerThanTheBudget(t *testing.T) {
	t.Cleanup(func() { maxBytes = MaxBytes })
	maxBytes = 10

	dir := Dir(t.TempDir())
	require.NoError(t, Append(dir, "/v1/traces", []byte("keep me")))

	// Accepting it would evict everything else to store one payload.
	err := Append(dir, "/v1/traces", []byte("way too much data for the budget"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	assert.Equal(t, 1, Len(dir), "the oversized payload must not have evicted anything")
}

func TestAppendRejectsAnUnknownOTLPPath(t *testing.T) {
	// The path is recovered from the filename on drain, so a path with no
	// suffix mapping would be unsendable once written.
	err := Append(Dir(t.TempDir()), "/v1/profiles", []byte("{}"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown OTLP path")
}

func TestAppendWithNoDirectoryConfiguredIsAnError(t *testing.T) {
	require.Error(t, Append("", "/v1/traces", []byte("{}")))
}

func TestDrainIgnoresPartialWritesAndForeignFiles(t *testing.T) {
	base := t.TempDir()
	dir := Dir(base)
	require.NoError(t, Append(dir, "/v1/traces", []byte("real")))

	// A temp from an Append that died mid-write, and something else entirely.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "0000000000000000001-1-1.traces.tmp"), []byte("partial"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("not mine"), 0o644))

	var calls []call
	sent, err := Drain(dir, 10, far(), collect(&calls))
	require.NoError(t, err)
	assert.Equal(t, 1, sent)
	assert.Equal(t, []call{{"/v1/traces", "real"}}, calls)

	// The unrecognized file is left alone rather than deleted.
	_, err = os.Stat(filepath.Join(dir, "README"))
	assert.NoError(t, err)
}
