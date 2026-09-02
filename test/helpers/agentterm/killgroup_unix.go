// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package agentterm

import (
	"syscall"
	"time"
)

// killGroup terminates the session's whole process group, then the group leader
// as a fallback.
//
// Killing only the child is not always enough: these CLIs are Node programs that
// spawn children, and a child that ignores SIGHUP survives the hangup the kernel
// sends when the session leader dies. It then keeps running, and keeps writing,
// after the test has finished.
//
// The pty starts the child with Setsid, so it is a process-group leader and its
// pid doubles as the group id. SIGTERM first, so a well-behaved agent can clean
// up, then SIGKILL for whatever ignored it.
//
// This does not reach a child that called setsid: that leaves the process group
// as well as the session, so no group-directed signal finds it. Catching those
// would mean walking the process tree, which is more than a test helper should
// carry.
func (s *Session) killGroup() {
	if s.cmd.Process == nil {
		return
	}
	pgid := s.cmd.Process.Pid

	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-time.After(2 * time.Second):
	case <-s.done: // the drain goroutine ends when the pty closes
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	_ = s.cmd.Process.Kill()
}
