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
func TestNormalizeRemoteURLStripsBenignUserinfo(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"ssh scheme carries a user", "ssh://git@github.com/dash0hq/probe.git", "https://github.com/dash0hq/probe"},
		{"scp form, default user", "git@github.com:dash0hq/probe.git", "https://github.com/dash0hq/probe"},
		{"scp form, custom user", "deploy@github.com:dash0hq/probe.git", "https://github.com/dash0hq/probe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeRemoteURL(tt.input))
		})
	}
}

// TestNormalizeRemoteURLGivesOneURLPerRepository pins the join key. Every
// spelling of one remote must export the same https URL: teammates on scp,
// https and ssh:// remotes work in one repository, and Dash0 joins agent spans
// to GitHub pull-request metrics by this value. The ssh:// form is common
// without anyone typing it: `url."ssh://git@github.com/".insteadOf
// https://github.com/` makes `git remote get-url` return it for an https
// remote.
func TestNormalizeRemoteURLGivesOneURLPerRepository(t *testing.T) {
	const want = "https://github.com/dash0hq/probe"
	for _, input := range []string{
		"https://github.com/dash0hq/probe.git",
		"git@github.com:dash0hq/probe.git",
		"ssh://git@github.com/dash0hq/probe.git",
		"ssh://github.com/dash0hq/probe",
		"SSH://git@github.com/dash0hq/probe.git",
		"git+ssh://git@github.com/dash0hq/probe.git",
		"ssh+git://git@github.com/dash0hq/probe.git",
		"ssh://git@github.com/dash0hq/probe/",
	} {
		t.Run(input, func(t *testing.T) {
			assert.Equal(t, want, normalizeRemoteURL(input))
		})
	}
}

// An SSH port is not the https port, so it comes off with the scheme. Other
// schemes keep their spelling: an https port or a local path still names the
// repository exactly.
func TestNormalizeRemoteURLOtherForms(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"ssh with port", "ssh://git@gitlab.example.com:2222/grp/probe.git", "https://gitlab.example.com/grp/probe"},
		{"ssh with credential", "ssh://deploy:s3cr3t@github.com/dash0hq/probe.git", "https://github.com/dash0hq/probe"},
		{"ssh without a path", "ssh://git@github.com", "https://github.com"},
		{"https with port", "https://gitlab.example.com:8443/grp/probe.git", "https://gitlab.example.com:8443/grp/probe"},
		{"git protocol", "git://github.com/dash0hq/probe.git", "git://github.com/dash0hq/probe"},
		{"local file", "file:///srv/git/probe.git", "file:///srv/git/probe"},
		{"local path", "/srv/git/probe.git", "/srv/git/probe"},
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

// TestDetectAtLinkedWorktree pins what PR attribution depends on: a linked
// worktree reports its own branch, not the main checkout's.
func TestDetectAtLinkedWorktree(t *testing.T) {
	main, worktree := repoWithWorktree(t, "feat/x")
	t.Chdir(t.TempDir())

	info := DetectAt(worktree)
	require.NotNil(t, info)
	assert.Equal(t, "feat/x", info.RefHeadName)
	assert.Equal(t, "branch", info.RefHeadType)
	assert.Equal(t, "https://github.com/acme/widgets", info.RepositoryURLFull)

	info = DetectAt(main)
	require.NotNil(t, info)
	assert.Equal(t, "main", info.RefHeadName)

	assert.Nil(t, DetectAt(t.TempDir()), "expected nil outside a git working tree")
	// A failed tool call can name a path that was never created, and git cannot
	// start in a directory that does not exist.
	assert.Nil(t, DetectAt(filepath.Join(t.TempDir(), "gone")), "expected nil for a missing directory")
}

// repoWithWorktree creates a repository on main with one commit and a linked
// worktree on branch, and returns both directories.
func repoWithWorktree(t *testing.T, branch string) (main, worktree string) {
	t.Helper()
	root := t.TempDir()
	main = filepath.Join(root, "main")
	worktree = filepath.Join(root, "wt")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", main},
		{"-C", main, "remote", "add", "origin", "git@github.com:acme/widgets.git"},
		{"-C", main, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", main, "worktree", "add", "-q", "-b", branch, worktree},
	} {
		out, err := exec.Command("git", args...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	return main, worktree
}

// TestDetectAtFollowsInsteadOf runs real git with the configuration that
// produces an ssh:// remote without the user typing one. The remote is the
// https URL from GitHub's clone button, and `git remote get-url` reports the
// rewritten ssh:// URL. The exported value must still be the https URL.
func TestDetectAtFollowsInsteadOf(t *testing.T) {
	dir := t.TempDir()
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(gitConfig, []byte("[url \"ssh://git@github.com/\"]\n\tinsteadOf = https://github.com/\n"), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "https://github.com/dash0hq/probe.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	require.Equal(t, "ssh://git@github.com/dash0hq/probe.git", gitOutput(dir, "remote", "get-url", "origin"),
		"precondition: git applies insteadOf to get-url")

	info := DetectAt(dir)
	require.NotNil(t, info)
	assert.Equal(t, "https://github.com/dash0hq/probe", info.RepositoryURLFull)
	assert.Equal(t, "dash0hq", info.OwnerName)
	assert.Equal(t, "probe", info.RepositoryName)
	assert.Equal(t, "github", info.ProviderName)
}
