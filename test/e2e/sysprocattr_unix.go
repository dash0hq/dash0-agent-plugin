// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e && !windows

package e2e

import (
	"os/exec"
	"syscall"
)

// setNewProcessGroup puts cmd in its own process group so a child's exit-time
// cleanup (which can signal its process group) cannot kill the test binary
// that spawned it.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
