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

// Detect reads the current git state and returns VCS info.
// Returns nil if not inside a git repository.
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
	} else {
		// Detached HEAD — check if on a tag.
		if tag := gitOutput("describe", "--tags", "--exact-match", "HEAD"); tag != "" {
			info.RefHeadName = tag
			info.RefHeadType = "tag"
		}
	}

	info.RefHeadRevision = gitOutput("rev-parse", "HEAD")
	info.UserName = gitOutput("config", "user.name")
	info.UserEmail = gitOutput("config", "user.email")

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
