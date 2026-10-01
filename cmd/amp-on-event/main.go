// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// amp-on-event accepts a completed turn from amp/index.ts.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/amp"
)

// The usage poll's total budget, and how long to wait before the first read.
//
// `amp threads export` does not lag incrementally. It returns the thread with
// *no messages at all* for the first few seconds after a turn ends, and then
// materializes the whole thing at once, complete with usage — measured around
// seven seconds past CLI exit, which is later still than agent.end. A single
// re-read after 1.5 s landed inside that empty window every time, which is why
// `partial` used to be the normal result on a real session rather than a rare
// one.
//
// So the loop waits before reading rather than after, and backs off by half
// each round. Each export costs roughly a second of CPU in a subprocess, and
// backing off covers the same window in four of them where a fixed one-second
// interval spent ten. Overshooting the window costs nothing: the loop stops as
// soon as usage matches, and amp/index.ts hands the turn off rather than
// waiting for this helper.
//
// Worst case for the whole helper is this window plus one in-flight export
// (exportTimeout) plus the shared OTLP retry budget.
var (
	usagePollDelay  = 2 * time.Second
	usagePollWindow = 20 * time.Second
	// A var so the subprocess test can separate "this export is slow" from
	// "this export hangs": under -race the controlled `amp` is a
	// race-instrumented copy of the test binary, and its startup alone can
	// approach the production budget on a loaded machine.
	exportTimeout = 5 * time.Second
)

// How many consecutive export *command* failures end the poll. One retry
// covers a transient hiccup; more than that is an environment that will
// not get better within the turn — `amp` missing from PATH, or logged out.
const exportFailureLimit = 2

func main() {
	if err := run(os.Stdin, exportThread); err != nil {
		// Do not log raw payloads, command stderr, or export contents.
		fmt.Fprintln(os.Stderr, "amp-on-event: telemetry unavailable")
		os.Exit(1)
	}
}

func run(input io.Reader, export func(string) ([]byte, error)) error {
	hn := harness.Amp
	if !hn.Enabled() {
		return nil
	}
	cfg := hn.Config()
	if cfg.OTLPUrl == "" && !cfg.Debug {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return fmt.Errorf("invalid envelope size")
	}
	var turn amp.Turn
	if err := json.Unmarshal(data, &turn); err != nil {
		return err
	}
	if err := turn.Validate(); err != nil {
		return err
	}
	var usage []amp.Usage
	status := "disabled"
	// On by default: models and token counts are the point of the integration,
	// and an install that silently accounts for nothing is the worse failure.
	// `export_usage: false` in either configuration file, or the environment
	// variable, turns the whole-thread read off for anyone who does not want it.
	if hn.PluginOptionBoolDefault("EXPORT_USAGE", true) {
		status = "unavailable"
		failures := 0
		deadline := time.Now().Add(usagePollWindow)
		// Sleep first: the export is reliably empty right after the turn. The
		// guard keeps the last sleep inside the window instead of past it.
		//
		// ReadUsage selects by the turn's assistant IDs alone, so with none of
		// them the verdict is "unavailable" whatever the export holds. Don't
		// spend the wait and a whole-thread read to reach it.
		for wait := usagePollDelay; len(turn.AssistantIDs) > 0 && !time.Now().Add(wait).After(deadline); wait += wait / 2 {
			time.Sleep(wait)
			data, err := export(turn.ThreadID)
			if err != nil {
				failures++
				if failures >= exportFailureLimit {
					break
				}
				continue
			}
			failures = 0
			usage, status = amp.ReadUsage(data, turn.ThreadID, turn.AssistantIDs)
			// Only "partial" is worth waiting on: the thread has not finished
			// persisting. "unsupported" and "invalid" are verdicts about the
			// thread id and the bridge's own ids, and "matched" is done.
			if status != "partial" {
				break
			}
		}
	}
	req, err := amp.BuildTrace(turn, usage, status, cfg)
	if err != nil {
		return err
	}
	return otlp.SendTracesRequest(req, cfg)
}

// limitBuffer stops an oversized export without retaining the thread contents.
// Do not embed bytes.Buffer: its promoted ReadFrom lets io.Copy bypass Write.
type limitBuffer struct{ buffer bytes.Buffer }

func (b *limitBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 16<<20 {
		return 0, fmt.Errorf("amp export too large")
	}
	return b.buffer.Write(p)
}

func exportThread(id string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "amp", "threads", "export", id)
	cmd.WaitDelay = time.Second
	var out limitBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.buffer.Bytes(), nil
}
