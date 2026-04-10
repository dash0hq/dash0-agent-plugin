package vcs

import (
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
	assert.NotEmpty(t, info.UserName)
	assert.NotEmpty(t, info.UserEmail)
	t.Logf("UserName=%q UserEmail=%q", info.UserName, info.UserEmail)
}
