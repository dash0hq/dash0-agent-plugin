// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Markers delimiting the part of a bootstrap that must not diverge between
// agents. Everything above the opening marker is agent-specific: the doc
// comment, AGENT, VERSION, and the data-directory chain.
const (
	sharedBegin = "# >>> shared bootstrap"
	sharedEnd   = "# <<< shared bootstrap <<<"
)

// failOpenAgents are the agents whose bootstrap keeps the session alive on any
// error. Claude's is deliberately excluded: it uses `set -euo pipefail` and
// exits non-zero, and its cache filename is the unprefixed legacy one, so its
// body cannot be identical.
var failOpenAgents = []string{"cursor", "codex", "copilot"}

func bootstrapPath(t *testing.T, agent string) string {
	t.Helper()
	return filepath.Join(repoRoot(t), agent, agent+"-on-event.sh")
}

func readBootstrap(t *testing.T, agent string) string {
	t.Helper()
	body, err := os.ReadFile(bootstrapPath(t, agent))
	require.NoError(t, err)
	return string(body)
}

// sharedRegion returns the marker-delimited body, markers included.
func sharedRegion(t *testing.T, agent string) string {
	t.Helper()
	body := readBootstrap(t, agent)

	start := strings.Index(body, sharedBegin)
	require.NotEqual(t, -1, start, "%s-on-event.sh has no %q marker", agent, sharedBegin)
	end := strings.Index(body, sharedEnd)
	require.NotEqual(t, -1, end, "%s-on-event.sh has no %q marker", agent, sharedEnd)
	require.Less(t, start, end, "%s-on-event.sh has the markers in the wrong order", agent)

	return body[start : end+len(sharedEnd)]
}

// The three fail-open bootstraps carry one implementation. Nothing enforces that
// at runtime — each agent ships a single self-contained file, because Copilot's
// marketplace source is ./copilot and both installers fetch one file from a raw
// URL — so this test is what keeps a fix from landing in one and not the others.
func TestFailOpenBootstrapsShareOneImplementation(t *testing.T) {
	reference := sharedRegion(t, failOpenAgents[0])
	require.NotEmpty(t, strings.TrimSpace(reference))

	for _, agent := range failOpenAgents[1:] {
		assert.Equal(t, reference, sharedRegion(t, agent),
			"%s-on-event.sh has diverged from %s-on-event.sh inside the shared region — "+
				"apply the change to all of %v", agent, failOpenAgents[0], failOpenAgents)
	}
}

// The shared region must not name one agent, or copying it to the next one
// carries a wrong asset name that only shows up as a download 404.
func TestSharedRegionIsAgentAgnostic(t *testing.T) {
	region := sharedRegion(t, failOpenAgents[0])

	for _, agent := range failOpenAgents {
		assert.NotContains(t, region, agent+"-on-event",
			"the shared region names %s; derive the name from $AGENT instead", agent)
	}
}

// Every bootstrap declares what the shared region consumes. A missing one is a
// `set -u` failure on the first hook event, which fail_open then swallows.
func TestBootstrapsDeclareTheSharedInputs(t *testing.T) {
	for _, agent := range failOpenAgents {
		t.Run(agent, func(t *testing.T) {
			head := strings.SplitN(readBootstrap(t, agent), sharedBegin, 2)[0]

			assert.Contains(t, head, "AGENT=\""+agent+"\"")
			assert.Regexp(t, `(?m)^VERSION="[0-9]+\.[0-9]+\.[0-9]+"$`, head)
			assert.Regexp(t, `(?m)^BASE=`, head)
		})
	}
}

// A downloaded binary that cannot be verified is never executed. Claude is
// included: its body differs, but the policy must not.
func TestNoBootstrapRunsAnUnverifiedBinary(t *testing.T) {
	for _, agent := range append([]string{"claude"}, failOpenAgents...) {
		t.Run(agent, func(t *testing.T) {
			body := readBootstrap(t, agent)

			assert.Contains(t, body, "refusing to run an unverified binary",
				"no refusal for a download with no checksums.txt entry")
			assert.Contains(t, body, "no sha256 tool",
				"no refusal for a host with no hash tool")
			assert.Contains(t, body, "checksum mismatch",
				"no refusal for a download whose digest does not match")
		})
	}
}
