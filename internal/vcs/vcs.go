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
}

// Detect reads the current git state and returns VCS info, or nil when the CWD
// is not inside a git working tree.
//
// User identity is deliberately not part of this: it is not repository state
// and must be reported even outside a working tree. See internal/identity.
func Detect() *Info {
	if err := git("rev-parse", "--git-dir"); err != nil {
		return nil
	}

	info := &Info{}

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

// normalizeRemoteURL converts SSH URLs to HTTPS and reduces the remote to the
// parts that identify the repository, for a consistent vcs.repository.url.full
// value.
//
// A remote such as https://x-access-token:ghs_TOKEN@github.com/owner/repo is
// what a CI checkout or a manual clone produces. The URL is stamped on every
// span, so the userinfo comes off here: the repository identity is the reportable
// part, the credential is not. The query and the fragment go with it — neither
// identifies a repository, and a GitLab-style ?private_token= holds a credential
// just as userinfo does.
//
// The scrub is by position, not by value: every userinfo goes, including a plain
// git@. Recognizing which usernames are secrets would need an allowlist, and the
// next token that looks benign would walk straight through it.
func normalizeRemoteURL(remote string) string {
	remote = strings.TrimSpace(remote)

	// scp-form ([user@]host:path) is not a URL — url.Parse rejects it — so the
	// userinfo comes off here. git@github.com:owner/repo.git →
	// https://github.com/owner/repo. The '@' must precede the first ':' so that
	// a local Windows path (C:\src\repo) is left alone.
	if !strings.Contains(remote, "://") {
		if colon := strings.Index(remote, ":"); colon > 0 {
			if at := strings.LastIndex(remote[:colon], "@"); at >= 0 {
				remote = "https://" + strings.Replace(remote[at+1:], ":", "/", 1)
			}
		}
	}

	// https://oauth2:ghp_TOKEN@github.com/owner/repo?private_token=abc →
	// https://github.com/owner/repo. Only rewrite when there is something to
	// remove, so every other remote keeps the exact spelling git reports.
	if u, err := url.Parse(remote); err == nil &&
		(u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "") {
		u.User = nil
		u.RawQuery = ""
		u.ForceQuery = false
		u.Fragment = ""
		remote = u.String()
	}

	// After the scrub, not before it: repo.git?private_token=abc does not end in
	// ".git" until the query is gone.
	remote = strings.TrimSuffix(remote, ".git")

	// A remote we failed to parse must never ship a credential. Report nothing
	// rather than a secret.
	if mayCarryCredential(remote) {
		return ""
	}
	return remote
}

// mayCarryCredential reports whether remote could still hold a secret: a "user@"
// in the authority, or a query or fragment. A scrubbed URL has none of these, so
// this only fires on a remote url.Parse rejected. The path is exempt, where an
// '@' is legitimate.
func mayCarryCredential(remote string) bool {
	if strings.ContainsAny(remote, "?#") {
		return true
	}
	authority := remote
	if _, rest, ok := strings.Cut(remote, "://"); ok {
		authority = rest
	}
	authority, _, _ = strings.Cut(authority, "/")
	return strings.Contains(authority, "@")
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
