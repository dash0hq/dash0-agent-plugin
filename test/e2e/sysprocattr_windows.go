// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e && windows

package e2e

import (
	"os/exec"
	"syscall"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP from the Windows API.
// Inlined as a constant to avoid pulling in golang.org/x/sys/windows for one
// value.
const createNewProcessGroup = 0x00000200

// setNewProcessGroup puts cmd in its own process group so a child's exit-time
// cleanup cannot kill the test binary that spawned it. This is the Windows
// analog of Setpgid on Unix.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}
