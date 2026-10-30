// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRemoteURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"git@github.com:dash0hq/dash0-agent-plugin.git", "https://github.com/dash0hq/dash0-agent-plugin"},
		{"https://github.com/dash0hq/dash0-agent-plugin.git", "https://github.com/dash0hq/dash0-agent-plugin"},
		{"https://github.com/dash0hq/dash0-agent-plugin", "https://github.com/dash0hq/dash0-agent-plugin"},
		{"git@gitlab.com:org/project.git", "https://gitlab.com/org/project"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, normalizeRemoteURL(tt.input))
	}
}

// TestNormalizeRemoteURLStripsCredentials pins the rule that a credential
// embedded in the remote never reaches vcs.repository.url.full. CI checkouts and
// manual clones routinely produce such a remote, and the value is stamped on
// every chat and tool span.
func TestNormalizeRemoteURLStripsCredentials(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		secret string
	}{
		{
			name:   "https with user and password",
			input:  "https://oauth2:ghp_SECRETTOKEN@github.com/dash0hq/probe.git",
			want:   "https://github.com/dash0hq/probe",
			secret: "ghp_SECRETTOKEN",
		},
		{
			name:   "gitlab personal access token",
			input:  "https://someuser:s3cr3t-pat@gitlab.com/grp/probe.git",
			want:   "https://gitlab.com/grp/probe",
			secret: "s3cr3t-pat",
		},
		{
			name:   "github app installation token",
			input:  "https://x-access-token:ghs_INSTALLTOKEN@github.com/dash0hq/probe.git",
			want:   "https://github.com/dash0hq/probe",
			secret: "ghs_INSTALLTOKEN",
		},
		{
			name:   "userinfo without a password",
			input:  "https://ghp_BARETOKEN@github.com/dash0hq/probe.git",
			want:   "https://github.com/dash0hq/probe",
			secret: "ghp_BARETOKEN",
		},
		{
			name:   "token in the query string",
			input:  "https://gitlab.com/grp/probe.git?private_token=s3cr3t-pat",
			want:   "https://gitlab.com/grp/probe",
			secret: "s3cr3t-pat",
		},
		{
			name:   "token in both userinfo and query",
			input:  "https://oauth2:ghp_SECRETTOKEN@github.com/dash0hq/probe.git?token=ghs_INSTALLTOKEN",
			want:   "https://github.com/dash0hq/probe",
			secret: "TOKEN",
		},
		{
			name:   "unparseable remote is dropped rather than leaked",
			input:  "https://oauth2:ghp_SECRETTOKEN@github.com:notaport/dash0hq/probe.git",
			want:   "",
			secret: "ghp_SECRETTOKEN",
		},
		{
			name:   "unparseable remote with a query token is dropped too",
			input:  "https://github.com:notaport/dash0hq/probe.git?private_token=s3cr3t-pat",
			want:   "",
			secret: "s3cr3t-pat",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeRemoteURL(tt.input)
			assert.NotContains(t, got, tt.secret, "credential must not survive normalization")
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestNormalizeRemoteURLStripsBenignUserinfo covers the userinfo that is not a
// secret. It comes off anyway: the scrub is by position, not by value, because
// telling a username from a token would need an allowlist that the next
// benign-looking token walks straight through.
//
// The accepted cost is that ssh://git@host/o/r now emits ssh://host/o/r, so the
// value changes for an SSH user who never embedded a credential and no longer
// round-trips through `git clone`. That is the same trade the scp form has always
// made: it drops git@ on its way to https.
func TestNormalizeRemoteURLStripsBenignUserinfo(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"ssh scheme carries a user", "ssh://git@github.com/dash0hq/probe.git", "ssh://github.com/dash0hq/probe"},
		{"scp form, default user", "git@github.com:dash0hq/probe.git", "https://github.com/dash0hq/probe"},
		{"scp form, custom user", "deploy@github.com:dash0hq/probe.git", "https://github.com/dash0hq/probe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeRemoteURL(tt.input))
		})
	}
}

// TestNormalizeRemoteURLKeepsDerivedAttributes checks that owner, repo and
// provider still resolve from a stripped URL, so removing the credential does
// not cost the other vcs.* attributes.
func TestNormalizeRemoteURLKeepsDerivedAttributes(t *testing.T) {
	for _, input := range []string{
		"https://oauth2:ghp_SECRETTOKEN@github.com/dash0hq/probe.git",
		"https://ghp_BARETOKEN@github.com/dash0hq/probe.git",
		"ssh://git@github.com/dash0hq/probe.git",
		"deploy@github.com:dash0hq/probe.git",
		"https://github.com/dash0hq/probe.git?private_token=s3cr3t-pat",
	} {
		t.Run(input, func(t *testing.T) {
			normalized := normalizeRemoteURL(input)
			owner, repo := parseOwnerRepo(normalized)
			assert.Equal(t, "dash0hq", owner)
			assert.Equal(t, "probe", repo)
			assert.Equal(t, "github", parseProvider(normalized))
		})
	}
}

func TestParseOwnerRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
	}{
		{"https://github.com/dash0hq/dash0-agent-plugin", "dash0hq", "dash0-agent-plugin"},
		{"https://gitlab.com/org/sub/project", "org", "sub"},
		{"https://github.com/owner/repo/extra/path", "owner", "repo"},
		{"not-a-url", "", ""},
	}
	for _, tt := range tests {
		owner, repo := parseOwnerRepo(tt.input)
		assert.Equal(t, tt.wantOwner, owner, "owner for %s", tt.input)
		assert.Equal(t, tt.wantRepo, repo, "repo for %s", tt.input)
	}
}

func TestParseProvider(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/dash0hq/repo", "github"},
		{"https://gitlab.com/org/repo", "gitlab"},
		{"https://bitbucket.org/team/repo", "bitbucket"},
		{"https://gitea.example.com/user/repo", "gitea"},
		{"https://custom-git.example.com/repo", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, parseProvider(tt.input), "provider for %s", tt.input)
	}
}

func TestDetect(t *testing.T) {
	// This test runs inside the dash0-agent-plugin repo itself,
	// so Detect() should return real values.
	info := Detect()
	require.NotNil(t, info, "expected VCS info (running inside a git repo)")

	assert.NotEmpty(t, info.RefHeadRevision)
	assert.GreaterOrEqual(t, len(info.RefHeadRevision), 40, "expected a full SHA")
	assert.NotEmpty(t, info.RepositoryURLFull)
	assert.NotEmpty(t, info.RepositoryName)
	assert.NotEmpty(t, info.OwnerName)
}

// TestDetectOutsideRepository pins the contract identitySpanAttributes relies
// on: Detect reports repository state only, and says nothing outside a repo.
func TestDetectOutsideRepository(t *testing.T) {
	t.Chdir(t.TempDir())
	assert.Nil(t, Detect(), "expected nil outside a git working tree")
}

// newRepoWithWorktree builds a repository on main with a linked worktree on
// feat/x, and returns both paths. A linked worktree is the case that motivated
// DetectAt: its .git is a file pointing into the parent's .git/worktrees, and
// its HEAD is a different branch from the checkout that owns the objects.
func newRepoWithWorktree(t *testing.T) (main, worktree string) {
	t.Helper()
	main = t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// A developer's init.defaultBranch, user identity or commit signing
		// would otherwise decide what this test observes.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	run(main, "init", "--initial-branch=main", ".")
	run(main, "remote", "add", "origin", "https://github.com/dash0hq/detectat-fixture")
	require.NoError(t, os.WriteFile(filepath.Join(main, "a.txt"), []byte("a"), 0o644))
	run(main, "add", "a.txt")
	run(main, "commit", "-m", "first")
	worktree = filepath.Join(t.TempDir(), "wt")
	run(main, "worktree", "add", "-b", "feat/x", worktree)
	return main, worktree
}

func TestDetectAtReadsTheNamedDirectory(t *testing.T) {
	main, worktree := newRepoWithWorktree(t)

	// Deliberately somewhere else entirely, which is the situation this
	// exists for: the process's directory must not decide the answer.
	t.Chdir(t.TempDir())
	require.Nil(t, Detect(), "precondition: the current directory is not a repository")

	inWorktree := DetectAt(worktree)
	require.NotNil(t, inWorktree)
	assert.Equal(t, "feat/x", inWorktree.RefHeadName)
	assert.Equal(t, "branch", inWorktree.RefHeadType)
	assert.Equal(t, "https://github.com/dash0hq/detectat-fixture", inWorktree.RepositoryURLFull)

	inMain := DetectAt(main)
	require.NotNil(t, inMain)
	assert.Equal(t, "main", inMain.RefHeadName)
	assert.Equal(t, "https://github.com/dash0hq/detectat-fixture", inMain.RepositoryURLFull)

	// Same objects, different checked-out ref: a test that only compared URLs
	// would pass with the branch still read from the wrong directory.
	assert.NotEqual(t, inMain.RefHeadName, inWorktree.RefHeadName)
}

func TestDetectAtOutsideRepositoryAndMissingDirectory(t *testing.T) {
	assert.Nil(t, DetectAt(t.TempDir()), "expected nil outside a working tree")
	assert.Nil(t, DetectAt(filepath.Join(t.TempDir(), "gone")), "expected nil for a missing directory")
}
