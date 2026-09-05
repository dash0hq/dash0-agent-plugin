// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package agentterm

import (
	"os/exec"
	"strconv"
)

// killGroup terminates the session's whole process tree.
//
// Windows has no process group a signal can address, and killing the child alone
// leaves what it spawned: these CLIs are Node programs, and an orphaned node.exe
// holds the pty open and keeps writing after the test has finished.
//
// taskkill /T walks the tree by parent pid, which is the mechanism Windows does
// offer. /F because a console process under a ConPTY has no SIGTERM equivalent
// to honour, so unlike the POSIX side there is nothing gentler to try first and
// no two-stage escalation to make.
func (s *Session) killGroup() {
	if s.cmd.Process == nil {
		return
	}
	pid := strconv.Itoa(s.cmd.Process.Pid)
	if out, err := exec.Command("taskkill", "/T", "/F", "/PID", pid).CombinedOutput(); err != nil {
		s.t.Logf("taskkill on pid %s: %v\n%s", pid, err, out)
	}
	_ = s.cmd.Process.Kill()
}
