// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package gitrepo makes a throwaway directory look like a checkout, so the
// plugin's VCS layer has a repository to read a turn's identity from.
package gitrepo

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// Init makes dir a git repo with one empty commit and a repo-local identity.
//
// The identity is repo-local on purpose: the plugin resolves a turn's user from
// the repository before it falls back to the OS account, so without this a span
// carries the name of whoever ran the test.
func Init(t *testing.T, dir string) {
	t.Helper()
	run(t, dir,
		[]string{"init", "-q"},
		[]string{"config", "user.email", "e2e@dash0.com"},
		[]string{"config", "user.name", "Dash0 E2E"},
		[]string{"commit", "-q", "--allow-empty", "-m", "init"},
	)
}

// run drives git in dir with the ambient configuration switched off.
//
// A developer with commit.gpgsign = true signs this throwaway commit too, and the
// commit fails outright when the key is not available — which is most of the time
// on a machine where the agent is locked. core.hooksPath and init.templateDir are
// the same class of input.
func run(t *testing.T, dir string, cmds ...[]string) {
	t.Helper()
	for _, args := range cmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// os.DevNull, so this is NUL on Windows.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull,
			"GIT_CONFIG_SYSTEM="+os.DevNull,
		)
		// The output, because cmd.Run() alone reports "exit status 128" and drops
		// the reason.
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}
