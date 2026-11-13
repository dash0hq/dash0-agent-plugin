// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// pi-on-event accepts a completed turn from pi/index.ts and exports it.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/pi"
)

func main() {
	if err := run(os.Stdin); err != nil {
		// Do not log raw payloads or export contents.
		fmt.Fprintln(os.Stderr, "pi-on-event: telemetry unavailable")
		os.Exit(1)
	}
}

func run(input io.Reader) error {
	hn := harness.Pi
	if !hn.Enabled() {
		return nil
	}
	cfg := hn.Config()
	if cfg.OTLPUrl == "" && !cfg.Debug {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(input, pi.MaxEnvelopeBytes+1))
	if err != nil {
		return fmt.Errorf("reading Pi turn: %w", err)
	}
	if len(data) > pi.MaxEnvelopeBytes {
		return fmt.Errorf("pi turn over %d bytes", pi.MaxEnvelopeBytes)
	}
	var turn pi.Turn
	if err := json.Unmarshal(data, &turn); err != nil {
		return fmt.Errorf("decoding Pi turn: %w", err)
	}
	req, err := pi.BuildTrace(turn, cfg)
	if err != nil {
		return fmt.Errorf("building Pi turn: %w", err)
	}
	if err := otlp.SendTracesRequest(req, cfg); err != nil {
		return fmt.Errorf("exporting Pi turn: %w", err)
	}
	return nil
}
