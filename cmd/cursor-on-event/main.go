// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// cursor-on-event is the Cursor-side entrypoint. Cursor spawns this binary
// fresh for every hook event (via cursor/cursor-on-event.sh which downloads
// the matching release on first run), pipes the hook JSON in on stdin, and
// expects a clean exit. The binary:
//
//  1. Reads the Cursor hook payload from stdin.
//  2. Normalizes it to the pipeline's canonical event vocabulary.
//  3. Hands off to pipeline.Process, which writes scratch state, manages
//     trace context across hook invocations, and emits OTLP spans.
//
// Telemetry failures never break the user's agent loop: errors are logged to
// stderr and the process exits 0.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/cursor"
)

var hn = harness.Cursor

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cursor-on-event: %v\n", err)
	}
}

func run() error {
	if !hn.Enabled() {
		return nil
	}

	dotenv.Load(".env")

	dataDir, err := hn.DataDir()
	if err != nil {
		return err
	}

	event, err := pipeline.ReadEvent(os.Stdin)
	if err != nil {
		return err
	}

	// Cursor spawns hooks with a CWD that isn't the workspace root, so
	// vcs.Detect()'s `git rev-parse --git-dir` would fail and we'd lose
	// repo/branch metadata. Every payload carries `workspace_roots`; chdir
	// into the first entry before normalization so git commands in the
	// pipeline see the right working tree.
	chdirToWorkspaceRoot(event)

	event = cursor.Normalize(event)
	if event == nil {
		return nil
	}

	cfg := hn.Config()
	now := time.Now().UTC()
	result, err := pipeline.Process(event, cfg, dataDir, now)
	if err != nil {
		return err
	}

	// Cursor's observational hooks ignore stdout for fail-open hooks. Log
	// status messages to stderr instead so they appear in Cursor's hook log
	// without affecting the agent loop.
	for _, msg := range result.Messages {
		if msg.UserText != "" {
			fmt.Fprintln(os.Stderr, msg.UserText)
		}
	}

	return nil
}

// chdirToWorkspaceRoot moves the process into the first workspace root from
// the Cursor hook payload. Best-effort: if the field is missing, not a list
// of strings, or chdir fails, we keep the original CWD and let vcs.Detect
// produce what it can.
func chdirToWorkspaceRoot(event map[string]any) {
	roots, ok := event["workspace_roots"].([]any)
	if !ok || len(roots) == 0 {
		return
	}
	root, ok := roots[0].(string)
	if !ok || root == "" {
		return
	}
	_ = os.Chdir(root)
}
