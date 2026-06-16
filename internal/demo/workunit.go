package demo

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// Persona is a synthetic team member whose git identity is attached to a
// session's spans (user.name / user.email come from `git config` in the cwd).
type Persona struct {
	Name  string
	Email string
	Model string
}

// WorkUnit is the shared fact that drives both the plugin session emitter and
// the (future) GitHub-App VCS span emitter. Both sides read repo/branch/commit/PR
// from the same WorkUnit so the S2M-derived metrics join on the VCS attributes.
type WorkUnit struct {
	Persona Persona
	Repo    Repo
	Branch  string
	Commits []string // one or more SHAs produced by the session
	PR      *PullRequest
}

// Repo describes the git repository — maps directly to the dash0.gen_ai.vcs.*
// resource/span attributes the plugin derives from `git remote` + parsing.
type Repo struct {
	Owner    string // e.g. "acme"
	Name     string // e.g. "web"
	Provider string // "github" | "gitlab" | "bitbucket"
}

// URL returns the HTTPS clone URL (without .git suffix) — the value that lands
// in dash0.gen_ai.vcs.repository.url.full.
func (r Repo) URL() string {
	host := "github.com"
	switch r.Provider {
	case "gitlab":
		host = "gitlab.com"
	case "bitbucket":
		host = "bitbucket.org"
	}
	return fmt.Sprintf("https://%s/%s/%s", host, r.Owner, r.Name)
}

// PullRequest holds the PR artifact a session produced. When present, the
// plugin extracts it from a tool response and emits dash0.gen_ai.vcs.pull_request.url.
type PullRequest struct {
	Number int
	URL    string
}

// ModelPool is a weighted set of models sampled per session.
type ModelPool []WeightedModel

// WeightedModel pairs a model ID with a relative weight for sampling.
type WeightedModel struct {
	ID     string
	Weight int
}

// DefaultModelPool is a realistic mix of models for demo data.
var DefaultModelPool = ModelPool{
	{ID: "claude-sonnet-4-6", Weight: 50},
	{ID: "claude-opus-4-8", Weight: 20},
	{ID: "claude-opus-4-8[1m]", Weight: 10},
	{ID: "claude-haiku-4-5", Weight: 20},
}

// Pick samples a model from the pool using the relative weights.
func (pool ModelPool) Pick() string {
	total := 0
	for _, m := range pool {
		total += m.Weight
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(total)))
	if err != nil {
		return pool[0].ID
	}
	idx := int(n.Int64())
	for _, m := range pool {
		idx -= m.Weight
		if idx < 0 {
			return m.ID
		}
	}
	return pool[len(pool)-1].ID
}

// TokenScale returns a multiplier for synthetic token counts based on model.
// Opus sessions are heavier (longer context, more output); Haiku is light.
func TokenScale(model string) float64 {
	switch {
	case model == "claude-opus-4-8[1m]":
		return 2.5
	case model == "claude-opus-4-8":
		return 1.8
	case model == "claude-haiku-4-5":
		return 0.4
	default:
		return 1.0
	}
}

// DefaultPersonas is a sample team for demo data.
var DefaultPersonas = []Persona{
	{Name: "Alice Chen", Email: "alice@acme.dev"},
	{Name: "Bob Martinez", Email: "bob@acme.dev"},
	{Name: "Carol Park", Email: "carol@acme.dev"},
	{Name: "Dave Kowalski", Email: "dave@acme.dev"},
	{Name: "Eve Nakamura", Email: "eve@acme.dev"},
}

// DefaultRepo is the synthetic repository the demo sessions operate on.
var DefaultRepo = Repo{Owner: "acme", Name: "web", Provider: "github"}

// DefaultBranches provides variety in branch names across sessions.
var DefaultBranches = []string{
	"feat/agent-sessions-table",
	"feat/dashboard-filters",
	"fix/auth-token-refresh",
	"feat/onboarding-flow",
	"fix/query-timeout",
	"feat/export-csv",
	"chore/upgrade-deps",
	"feat/notification-preferences",
}
