// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package vcs

import (
	"net/url"
	"os/exec"
	"strings"
)

// Info holds VCS attributes derived from a git repository.
type Info struct {
	RepositoryURLFull string // vcs.repository.url.full
	RepositoryName    string // vcs.repository.name
	OwnerName         string // vcs.owner.name
	ProviderName      string // vcs.provider.name
	RefHeadName       string // vcs.ref.head.name
	RefHeadRevision   string // vcs.ref.head.revision
	RefHeadType       string // vcs.ref.head.type
	UserName          string // user.name
	UserEmail         string // user.email
}

// Detect reads the current git state and returns VCS info. User identity
// (UserName / UserEmail) is collected regardless of CWD — `git config user.*`
// walks system → global → local, so global config still works outside a
// working tree. This matters for Cursor: it spawns hooks with a CWD that
// isn't always a git working directory, but the user's global git identity
// is still the right answer.
//
// Returns nil only when neither repository info nor user identity is
// available — i.e. git is not installed or has no usable config at all.
func Detect() *Info {
	info := &Info{
		UserName:  gitOutput("config", "user.name"),
		UserEmail: gitOutput("config", "user.email"),
	}

	if err := git("rev-parse", "--git-dir"); err == nil {
		if remote := gitOutput("remote", "get-url", "origin"); remote != "" {
			info.RepositoryURLFull = normalizeRemoteURL(remote)
			info.OwnerName, info.RepositoryName = parseOwnerRepo(info.RepositoryURLFull)
			info.ProviderName = parseProvider(info.RepositoryURLFull)
		}

		if branch := gitOutput("rev-parse", "--abbrev-ref", "HEAD"); branch != "" && branch != "HEAD" {
			info.RefHeadName = branch
			info.RefHeadType = "branch"
		} else if tag := gitOutput("describe", "--tags", "--exact-match", "HEAD"); tag != "" {
			info.RefHeadName = tag
			info.RefHeadType = "tag"
		}

		info.RefHeadRevision = gitOutput("rev-parse", "HEAD")
	}

	if *info == (Info{}) {
		return nil
	}
	return info
}

func git(args ...string) error {
	return exec.Command("git", args...).Run()
}

func gitOutput(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// normalizeRemoteURL converts SSH URLs to HTTPS for a consistent
// vcs.repository.url.full value.
func normalizeRemoteURL(remote string) string {
	remote = strings.TrimSpace(remote)

	// git@github.com:owner/repo.git → https://github.com/owner/repo
	if strings.HasPrefix(remote, "git@") {
		remote = strings.TrimPrefix(remote, "git@")
		remote = strings.Replace(remote, ":", "/", 1)
		remote = "https://" + remote
	}

	remote = strings.TrimSuffix(remote, ".git")
	return remote
}

// parseOwnerRepo extracts owner and repo name from an HTTPS URL.
// e.g. https://github.com/dash0hq/dash0-agent-plugin → ("dash0hq", "dash0-agent-plugin")
func parseOwnerRepo(httpsURL string) (owner, repo string) {
	u, err := url.Parse(httpsURL)
	if err != nil {
		return "", ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

// parseProvider extracts the VCS provider from the hostname.
// e.g. github.com → "github", gitlab.example.com → "gitlab"
func parseProvider(httpsURL string) string {
	u, err := url.Parse(httpsURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.Contains(host, "github"):
		return "github"
	case strings.Contains(host, "gitlab"):
		return "gitlab"
	case strings.Contains(host, "bitbucket"):
		return "bitbucket"
	case strings.Contains(host, "gitea"):
		return "gitea"
	default:
		return ""
	}
}
