// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// NoTeamPrefix opens the missing-team warning. Entrypoints match on it to append
// their own remediation, so a reworded message cannot silently lose it.
const NoTeamPrefix = "dash0: no team configured"

// setupNudge builds the SessionStart warning shown while TEAM_NAME is unset, and
// reports whether there is anything to say. Users leave it unset because nothing
// tells them it exists, and their spans then carry no dash0.team.name.
//
// TEAM_NAME is the only option checked, deliberately: a warning users learn to
// skip is worse than none. DATASET is the near miss — unset sends no
// Dash0-Dataset header, so the backend picks the dataset named "default", which is
// what the installers write anyway.
//
// UserText names the attribute and nothing else, because where to set the value
// differs per coding agent; each entrypoint appends its own instructions. Only
// Claude Code renders ModelContext, which is what makes the agent offer the
// dash0-configure skill instead of the user having to find it.
func setupNudge(cfg otlp.Config) (Message, bool) {
	if cfg.TeamName != "" {
		return Message{}, false
	}

	return Message{
		UserText: NoTeamPrefix + " — spans carry no dash0.team.name.",
		ModelContext: "The Dash0 agent plugin is running but TEAM_NAME is unset, so its spans cannot be " +
			"attributed to a team. The dash0-configure skill collects the value and writes the config file. " +
			"Offer to run it once, then leave the subject alone for the rest of this session unless the user " +
			"brings it up.",
	}, true
}
