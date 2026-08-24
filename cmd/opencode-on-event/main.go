// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// opencode-on-event is the OpenCode-side entrypoint. OpenCode has no hook
// mechanism that spawns a process, so an in-process TypeScript plugin filters
// the event bus and spawns this binary (via opencode/opencode-on-event.sh, which
// downloads the matching release on first run) once per event it consumes,
// piping a plugin envelope in on stdin. The binary:
//
//  1. Reads the envelope from stdin.
//  2. Normalizes it to the pipeline's canonical event vocabulary, dropping the
//     events that map to no span.
//  3. Hands off to pipeline.Process, which writes scratch state, manages trace
//     context across invocations, and emits OTLP spans.
//
// Telemetry failures never break the user's session: errors go to stderr and
// the process always exits 0.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/opencode"
)

var hn = harness.OpenCode

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "opencode-on-event: %v\n", err)
	}
}

func run() error {
	dotenv.Load(".env")

	dataDir, err := hn.DataDir()
	if err != nil {
		return err
	}

	envelope, err := pipeline.ReadEvent(os.Stdin)
	if err != nil {
		return err
	}

	// The plugin resolves the project directory and puts it on the envelope,
	// where the normalizer also copies it onto the canonical event. chdir before
	// normalization so it holds for every event, including the ones that carry
	// no cwd of their own.
	hn.ChdirToEventCwd(envelope)

	event := opencode.Normalize(envelope)
	if event == nil {
		return nil
	}

	result, err := pipeline.Process(event, hn.Config(), dataDir, time.Now().UTC())
	if err != nil {
		return err
	}

	// The plugin renders these as a TUI toast. It reads them from stderr,
	// because stdout carries nothing OpenCode consumes.
	for _, msg := range result.Messages {
		if msg.UserText != "" {
			fmt.Fprintln(os.Stderr, msg.UserText)
		}
	}

	return nil
}
