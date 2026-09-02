// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// The contracts every entrypoint shares, driven from here because run() is an
// unexported member of a package main. The cases, the environment and the
// assertions live in test/helpers/hookcheck, so all four are asserted once
// rather than four times.
package main

import (
	"testing"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/hookcheck"
)

func TestRunFailsOpenOnEveryTelemetryFailure(t *testing.T) {
	hookcheck.FailOpen(t, hookcheck.Cursor, run)
}

func TestTheConfiguredTokenReachesTheWire(t *testing.T) {
	hookcheck.Credentials(t, hookcheck.Cursor, run)
}

func TestDash0AuthTokenIsNotHonoured(t *testing.T) {
	hookcheck.Dash0AuthTokenIsNotHonoured(t, hookcheck.Cursor, run)
}
