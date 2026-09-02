// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package e2e runs a real agent CLI through a live turn with the plugin
// installed, and asserts what reaches the OTLP endpoint.
//
// This is the only package that proves an agent emits payloads in the shape the
// plugin parses. Every test here needs the agent's CLI and its credentials, and
// each FAILS rather than skips when either is missing, so a misconfigured secret
// is loud instead of quietly disabling the canary.
//
// Cheaper layers cover the rest: the unit suite drives the producers directly,
// test/marketplaces drives a real CLI through an install with no turn, and
// test/consistency reads the shipped files.
//
// # Why there is no Cursor canary
//
// A Cursor canary cannot be hermetic. Its stored login does not travel: a
// throwaway home carrying a copied cli-config.json still fails with
// `Authentication required`, and CURSOR_CONFIG_DIR moves the config directory
// without moving where hooks are read from (both measured 2026-09-01). A canary
// would have to register hooks in the machine's real ~/.cursor/hooks.json and
// run against the developer's own login, which is fine for a QA driver a person
// invokes and not for CI. The other three each have a credential a throwaway
// home can carry.
//
// Cursor is therefore covered as far as it can be without a session:
// test/installers runs its real installer and uninstaller, test/consistency
// reads its shipped files, and qa/specs/cursor drives real sessions on request.
// Add the canary when a Cursor login can be provisioned into a temp home.
//
// # Windows
//
// Windows runs every test here on its own CI leg. The driver reaches a terminal
// through go-pty, a pty on POSIX and a ConPTY on Windows. Two things differ: a
// throwaway home is named under USERPROFILE as well as HOME (testenv.Home), and
// Codex installs through install-codex.ps1, whose hook runs codex-on-event.ps1.
// Windows facts that need no live turn stay in test/consistency.
package e2e

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// turnTimeout bounds one typed turn: the model call, its tool calls, and the stop
// hook's export. A ceiling rather than an expectation, since a real turn takes
// tens of seconds.
const turnTimeout = 3 * time.Minute

// onboardingTimeout bounds a first-run dialog. These screens render as soon as
// the TUI starts, so waiting longer only delays the report that the wording
// changed.
const onboardingTimeout = 30 * time.Second

// sandboxTimeout bounds the sandbox Codex installs on Windows before it will
// accept a prompt. Unlike a dialog this is real work, and Codex says so ("this
// may take a few minutes"), though it has measured in seconds on a runner.
const sandboxTimeout = 2 * time.Minute

// spanTimeout bounds a span that trails the barrier that released a wait, rather
// than a whole turn: the process that exports it has already run, so this covers
// one in-flight HTTP round trip and not a model call.
const spanTimeout = 30 * time.Second

// sessionTimeout bounds a whole interactive session. It must exceed the turns a
// test types, or the context kills the agent mid-turn and the failure looks like
// missing telemetry rather than a budget that was too small. The sandbox is part
// of that budget: it runs before the first prompt is typed.
const sessionTimeout = 2*turnTimeout + sandboxTimeout + time.Minute

// TestMain loads the repo-root .env before any test runs, the same way every
// entrypoint calls dotenv.Load(".env") at startup. Without it a developer with a
// working .env has to export the tokens by hand.
//
// dotenv.Load sets only absent variables, so an exported value and a CI secret
// both win. The path comes from the repo root because a test binary runs with
// its own package directory as cwd.
func TestMain(m *testing.M) {
	root, err := pluginrepo.FindRoot()
	if err != nil {
		// Not fatal: the tests can still run on exported credentials, and each one
		// reports its own missing-credential failure with a better message.
		log.Printf("e2e: could not locate the repo root to load .env: %v", err)
	} else {
		dotenv.Load(filepath.Join(root, ".env"))
	}
	expandTempDir()
	os.Exit(m.Run())
}

// expandTempDir rewrites the process temp directory to its fully expanded form,
// so every t.TempDir() below hands an agent a working directory that matches the
// cwd the agent resolves for itself.
//
// GitHub's windows-latest runner runs as `runneradmin`, so %TEMP% is
// C:\Users\RUNNER~1\AppData\Local\Temp — an 8.3 short name, which every temp
// directory here then inherits. A cwd carrying one defeats Claude Code's
// --allowed-tools matching: the allow-listed Write is not recognized as
// allow-listed, the session stops on an interactive permission dialog nothing
// answers, and the only symptom is the span barrier reporting missing telemetry
// three minutes later. Measured both ways on one machine, same CLI and
// credentials: the long temp directory passes, its 8.3 alias hangs.
//
// Windows only, and EvalSymlinks is what expands the short components. On macOS
// it would also rewrite the temp directory through /private, changing paths for
// no reason here.
//
// Failing to expand is not fatal. The variables are the ones os.TempDir reads on
// Windows, and t.TempDir goes through it.
func expandTempDir() {
	if runtime.GOOS != "windows" {
		return
	}
	tmp := os.TempDir()
	long, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		log.Printf("e2e: could not expand the temp directory %q: %v", tmp, err)
		return
	}
	for _, name := range []string{"TMP", "TEMP"} {
		if err := os.Setenv(name, long); err != nil {
			log.Printf("e2e: could not set %s=%q: %v", name, long, err)
		}
	}
}
