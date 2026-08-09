// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package identity resolves the human behind a coding-agent session.
//
// The git config is the preferred source, but a developer who never ran
// `git config --global user.name` would otherwise be invisible for attribution
// while remaining fully visible for cost — every span carrying tokens and no
// name. So we fall back to the OS account, and record which source we used so
// the fallback stays auditable rather than silently indistinguishable from a
// real git identity.
package identity

import (
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// Source values for Info.Source.
const (
	SourceGit = "git"
	SourceOS  = "os"
)

// Info holds the resolved user identity.
type Info struct {
	Name   string // user.name
	Email  string // user.email — git only, never inferred
	Source string // SourceGit or SourceOS; empty when Name is empty
}

// ciEnvVars are set by CI systems. In CI the OS account is a shared build
// identity ("runner", "root"), so falling back to it would collapse every job
// in an organization into one bucket that looks like a single, impossibly
// productive developer. An absent attribute is the honest answer there.
var ciEnvVars = []string{
	"CI",
	"GITHUB_ACTIONS",
	"GITLAB_CI",
	"BUILDKITE",
	"JENKINS_URL",
}

// genericUsernames are shared or service accounts that identify a machine
// rather than a person. They carry the same aggregation hazard as CI, and
// catch container runs that set no CI variable at all.
var genericUsernames = map[string]bool{
	"root":    true,
	"runner":  true,
	"ubuntu":  true,
	"debian":  true,
	"node":    true,
	"admin":   true,
	"app":     true,
	"builder": true,
	"nobody":  true,
	"docker":  true,
}

// Resolve returns the current user identity, preferring git config and falling
// back to the OS account.
//
// Identity is collected regardless of CWD — `git config user.*` walks system →
// global → local, so global config still works outside a working tree, and the
// OS account never depends on CWD at all. This matters for Cursor: it spawns
// hooks with a CWD that isn't always a git working directory, but the user's
// identity is still the right answer.
func Resolve() Info {
	var osName, osUsername string
	if u, err := user.Current(); err == nil {
		osName, osUsername = u.Name, u.Username
	}

	return resolve(
		gitConfig("user.name"),
		gitConfig("user.email"),
		osName,
		osUsername,
		firstEnv("USER", "USERNAME", "LOGNAME"),
		isCI(),
	)
}

// resolve holds the fallback chain, separated from its I/O so the ordering and
// the guards can be tested exhaustively.
func resolve(gitName, gitEmail, osName, osUsername, envUser string, ci bool) Info {
	info := Info{Email: gitEmail}

	if gitName != "" {
		info.Name = gitName
		info.Source = SourceGit
		return info
	}

	// No git identity. Everything below is the OS fallback, which we only trust
	// to name a person on an interactive machine.
	if ci {
		return info
	}

	for _, candidate := range []string{osName, osUsername, envUser} {
		if usableName(candidate) {
			info.Name = strings.TrimSpace(candidate)
			info.Source = SourceOS
			return info
		}
	}

	return info
}

// usableName reports whether an OS-derived candidate names a person.
func usableName(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	return !genericUsernames[strings.ToLower(s)]
}

func isCI() bool {
	for _, key := range ciEnvVars {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func gitConfig(key string) string {
	out, err := exec.Command("git", "config", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
