// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package demo

import "fmt"

// Deterministic "canary" turns for validating Dash0's cost enrichment and the
// cost aggregations built on top of the coding_agent_spans projection.
//
// Where the random generator makes each turn's cost unknowable up front, the
// canary emits a fixed set of turns whose token usage — and therefore enriched
// cost — is a known constant. Each canary turn is attributed to a reserved user,
// repository, and team (all disjoint from the closed lists in data.go), so it
// can be filtered out of the random traffic. Filtering to a canary user, repo,
// or team then yields a hand-computable expected cost, which makes deviations
// easy to reason about.
//
// The three canary contributors share one token vector and differ only by model,
// so their per-turn costs land in an exact 5:3:1 ratio (Opus:Sonnet:Haiku) — a
// cheap cross-check that the aggregations and rate table are consistent.
//
// Canary emission is gated (DEMO_CANARY / -canary) so it only lands in the
// cost-validation dataset and never pollutes the customer-facing demo datasets.

const (
	// canaryOwner matches the owner used by the random repos in data.go.
	canaryOwner = "dash0hq"
	// canaryTeam groups the canary contributors so a team-level cost aggregation
	// rolls up to the exact sum of the three per-user costs.
	canaryTeam = "Cost Validation"
	// canaryBranch and canaryRevision are pinned so the VCS join surface is
	// stable for the canary turns too.
	canaryBranch   = "cost-canary-main"
	canaryRevision = "0000000000000000000000000000000000000000"
	// canaryEffort is fixed; effort does not affect cost but keeping it constant
	// keeps the canary turns fully reproducible on their reported dimensions.
	canaryEffort = "high"
)

// canaryUsage is the fixed per-turn token vector every canary turn reports.
// Chosen as round numbers that sit within the random traffic's range so the
// canary users don't dominate top-N cost charts. Identical across models, so
// the enriched cost differs only by the model's price tier.
var canaryUsage = usage{
	inputTokens:         100_000,
	outputTokens:        100_000,
	cacheCreationTokens: 100_000,
	cacheReadTokens:     1_000_000,
}

// canaryMessage is the fixed prompt/response pair on every canary turn.
var canaryMessage = messagePair{
	Input:  "Cost-validation canary: emit a turn with a pinned token vector so the enriched cost is a known constant.",
	Output: "Emitted a canary turn with fixed token usage; its cost is deterministic and filterable by the cost-canary user/repo/team.",
}

// canaryContributor pins one model tier to a reserved, filterable user and
// repository. The model strings must match those in data.go's models list.
type canaryContributor struct {
	user  string
	repo  string
	model string
}

var canaryContributors = []canaryContributor{
	{user: "cost-canary-opus", repo: "cost-canary-opus", model: "claude-opus-4-8"},
	{user: "cost-canary-sonnet", repo: "cost-canary-sonnet", model: "claude-sonnet-4-6"},
	{user: "cost-canary-haiku", repo: "cost-canary-haiku", model: "claude-haiku-4-5"},
}

// canaryTurns builds the deterministic canary turns — one per model tier. Cost
// and the aggregation-identity dimensions (user, repo, team, model, usage) are
// pinned; trace/span/session IDs stay unique so the turns still render as
// distinct sessions rather than collapsing into one ever-growing session.
func canaryTurns() []turn {
	turns := make([]turn, 0, len(canaryContributors))
	for _, c := range canaryContributors {
		turns = append(turns, turn{
			repo: repo{
				Owner:   canaryOwner,
				Name:    c.repo,
				URLFull: fmt.Sprintf("https://github.com/%s/%s", canaryOwner, c.repo),
			},
			user:     user{Name: c.user, Team: canaryTeam},
			model:    c.model,
			effort:   canaryEffort,
			msg:      canaryMessage,
			branch:   canaryBranch,
			revision: canaryRevision,
			session:  randomUUID(),
			usage:    canaryUsage,
		})
	}
	return turns
}
