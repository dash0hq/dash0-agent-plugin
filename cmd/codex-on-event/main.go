// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// codex-on-event is the OpenAI Codex-side entrypoint. Codex spawns this binary
// fresh for every hook event (via codex/codex-on-event.sh, which downloads the
// matching release on first run), pipes the hook JSON in on stdin, and expects a
// clean exit. The binary:
//
//  1. Reads the Codex hook payload from stdin.
//  2. Normalizes it to the pipeline's canonical event vocabulary (see
//     internal/source/codex). Codex's hook events already match that vocabulary
//     almost exactly, so normalization is nearly a passthrough — its only real
//     job is deriving tool-call duration, which Codex omits.
//  3. Hands off to pipeline.Process, which writes scratch state, manages trace
//     context across hook invocations, and emits OTLP spans.
//
// Telemetry failures never break the user's agent loop: errors are logged to
// stderr and the process exits 0.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/codex"
)

var hn = harness.Codex

func main() {
	// Install-time subcommand: emit the managed config.toml block (hook
	// registrations + reproduced trust hashes) for install-codex.sh to append.
	if len(os.Args) > 1 && os.Args[1] == "emit-codex-hooks" {
		if err := emitCodexHooks(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "codex-on-event: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "codex-on-event: %v\n", err)
	}
}

// emitCodexHooks prints the marker-delimited config.toml block the installer
// appends: --command is the exact hook command string, --config is the absolute
// path of the config file it will be written into (part of each trust key).
// existing config content is read from --config (if present) so pre-existing
// user hook groups are counted for correct trust-key indices.
func emitCodexHooks(args []string) error {
	fs := flag.NewFlagSet("emit-codex-hooks", flag.ContinueOnError)
	command := fs.String("command", "", "exact hook command string Codex will run")
	configPath := fs.String("config", "", "absolute path of the config.toml the block is written into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *command == "" || *configPath == "" {
		return fmt.Errorf("emit-codex-hooks requires --command and --config")
	}

	// Codex keys hook trust on its RESOLVED config path (it realpath's the file),
	// so the trust-state key must use the canonical path or Codex won't find our
	// entry and will treat the hook as untrusted. Resolve symlinks on the parent
	// dir (the file itself may not exist yet) and rejoin the filename.
	keyPath := *configPath
	if resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(*configPath)); err == nil {
		keyPath = filepath.Join(resolvedDir, filepath.Base(*configPath))
	}

	// Read existing config minus any prior managed block, so re-installs count
	// only the user's own hook groups.
	var existing string
	if data, err := os.ReadFile(*configPath); err == nil {
		existing = codex.StripManagedBlock(string(data))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", *configPath, err)
	}

	block, err := codex.RenderManagedBlock(keyPath, *command, existing)
	if err != nil {
		return err
	}
	fmt.Print(block)
	return nil
}

func run() error {
	dotenv.Load(".env")

	dataDir, err := hn.DataDir()
	if err != nil {
		return err
	}

	event, err := pipeline.ReadEvent(os.Stdin)
	if err != nil {
		return err
	}

	// Codex hooks carry the workspace as `cwd`. Codex may spawn the hook with a
	// different process CWD, so chdir into the payload's cwd before normalization
	// so vcs.Detect()'s git commands see the right working tree.
	hn.ChdirToEventCwd(event)

	// Normalization needs the per-session scratch dir to back-calculate tool-call
	// duration from the matching PreToolUse it logged earlier. Compute it the same
	// way pipeline.Process does so both agree on the path.
	sessionID, _ := event["session_id"].(string)
	sessionDir := pipeline.SessionDir(dataDir, sessionID)

	now := time.Now().UTC()
	event = codex.Normalize(event, sessionDir, now)
	if event == nil {
		return nil
	}

	cfg := hn.Config()
	result, err := pipeline.Process(event, cfg, dataDir, now)
	if err != nil {
		return err
	}

	// Codex ignores stdout for observational hooks and does not surface hook
	// stderr in the TUI or any documented log, so this is best-effort diagnostic
	// output only (visible when running the binary directly or in the e2e
	// harness); it never affects the agent loop.
	for _, msg := range result.Messages {
		if msg.UserText != "" {
			fmt.Fprintln(os.Stderr, msg.UserText)
		}
	}

	return nil
}
