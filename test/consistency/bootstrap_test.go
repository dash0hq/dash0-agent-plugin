// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// badPayload is a stdin no entrypoint can parse. Every one of them reports it as
// "parsing JSON from stdin", which is what lets the exit-code contracts in
// bootstrap_unix_test.go and bootstrap_windows_test.go prove the staged binary
// ran rather than that the bootstrap failed open before reaching it.
const badPayload = `not json`

// The contracts that read a .sh bootstrap as text, and the fixtures every
// bootstrap check shares. Reading text, so they run on every platform.
//
// powershell_test.go is the same thing for the .ps1 files. The two
// bootstrap_<goos>_test.go files hold the contracts that run a bootstrap
// instead, which is what confines them to one host.
//
// An accessor more than one subject reads sits on Agent in agents_test.go, with
// the row it answers about. What stays here parses shell and is read nowhere else.

// Markers delimiting the part of a bootstrap that must not diverge between
// agents. Everything above the opening marker is agent-specific: the doc
// comment, AGENT, VERSION, and the data-directory chain.
const (
	sharedBegin = "# >>> shared bootstrap"
	sharedEnd   = "# <<< shared bootstrap <<<"
)

// sharedRegion returns the marker-delimited body of a file, markers included.
func sharedRegion(t *testing.T, name, body string) string {
	t.Helper()

	start := strings.Index(body, sharedBegin)
	require.NotEqual(t, -1, start, "%s has no %q marker", name, sharedBegin)
	end := strings.Index(body, sharedEnd)
	require.NotEqual(t, -1, end, "%s has no %q marker", name, sharedEnd)
	require.Less(t, start, end, "%s has the markers in the wrong order", name)

	return body[start : end+len(sharedEnd)]
}

// shellRegion is the shared region of an agent's POSIX bootstrap.
func (a Agent) shellRegion(t *testing.T) string {
	t.Helper()
	return sharedRegion(t, a.Bootstrap, a.bootstrapBody(t))
}

// releaseAsset is the release asset a bootstrap downloads on this host.
//
// This is NOT the cache filename; see cacheName. The asset carries no version and
// the cached copy does, so staging a file under this name leaves the cache cold and
// every assertion about it passes having measured nothing.
func (a Agent) releaseAsset() string {
	return fmt.Sprintf("%s-%s-%s%s", a.AssetStem, runtime.GOOS, runtime.GOARCH, pluginrepo.ExeSuffix())
}

// cacheName is the filename a bootstrap looks for under its bin directory, which
// is the asset name with VERSION spliced in.
func (a Agent) cacheName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%s-%s-%s-%s%s",
		a.CacheStem, a.bootstrapVersion(t), runtime.GOOS, runtime.GOARCH, pluginrepo.ExeSuffix())
}

// Every bootstrap fetches the asset name the release actually publishes.
//
// This is the one link the Go tests could not see. The fixtures serve whatever
// releaseAsset() names, so a descriptor agreeing with itself proves nothing about
// the release, and a bootstrap asking for a name goreleaser stopped building
// fails open: no binary, no telemetry, no error the user sees.
//
// It has happened. The CI step this package replaced was written for it, and its
// comment recorded the shape: the script asked for claude-on-event-<os>-<arch>
// while the probe looked for on-event-<os>-<arch>, and the probe passed.
//
// Static, and deliberately so. The old check curl'd the real release, which
// needed the network and could only run once a tag existed. Reading
// .goreleaser.yaml catches the same rename at the commit that makes it.
func TestBootstrapsFetchTheAssetTheReleasePublishes(t *testing.T) {
	body, err := os.ReadFile(abs(t, ".goreleaser.yaml"))
	require.NoError(t, err)
	release := string(body)

	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			require.NotEmpty(t, a.AssetStem, "%s declares no AssetStem", a.Label)
			require.NotEmpty(t, a.CacheStem, "%s declares no CacheStem", a.Label)

			assert.Contains(t, release,
				fmt.Sprintf(`binary: "%s-{{ .Os }}-{{ .Arch }}"`, a.AssetStem),
				"no goreleaser build publishes %s-<os>-<arch>, so this runtime's bootstrap "+
					"asks for an asset that will not exist", a.AssetStem)

			// And the bootstrap has to ask for that stem, or the descriptor agrees
			// with the release while the script fetches something else.
			//
			// The three that share a body build the name from $AGENT, which
			// TestSharedRegionIsAgentAgnostic requires of them, so what is checked
			// there is that the stem is $AGENT's expansion. AGENT="<label>" itself is
			// pinned by TestBootstrapsDeclareTheSharedInputs.
			wantShell := a.AssetStem + "-${OS}-${ARCH}"
			if a.SharesBootstrapBody {
				require.Equal(t, a.Label+"-on-event", a.AssetStem,
					"this bootstrap derives its asset name from $AGENT, so AssetStem must be "+
						"the label plus -on-event")
				wantShell = "${AGENT}-on-event-${OS}-${ARCH}"
			}
			assert.Contains(t, a.bootstrapBody(t), wantShell,
				"%s does not fetch %s-<os>-<arch>", a.Bootstrap, a.AssetStem)

			if a.WindowsBootstrap == "" {
				return
			}
			// The PowerShell twin fetches the same asset, hardcoding the platform
			// because it only ever runs on one.
			assert.Contains(t, a.windowsBootstrapBody(t), `$Agent-on-event-windows-$Arch.exe`,
				"%s does not fetch %s-windows-<arch>.exe", a.WindowsBootstrap, a.AssetStem)
		})
	}
}

// Claude keeps fetching its pre-v0.1.25 asset name as a fallback.
//
// Its cache filename is unversioned-stem on purpose, so an install from before
// the rename finds its binary and does not re-download. Dropping the fallback
// would strand a pinned older VERSION: the primary name does not exist in those
// releases. Removing it is a decision, so it should fail here first.
func TestClaudeStillFetchesItsLegacyAssetName(t *testing.T) {
	claude := agentByLabel(t, "claude")
	assert.Contains(t, claude.bootstrapBody(t), claude.CacheStem+"-${OS}-${ARCH}",
		"%s no longer falls back to %s-<os>-<arch>, which is the only name releases "+
			"before v0.1.25 carry", claude.Bootstrap, claude.CacheStem)
}

// The three fail-open bootstraps carry one implementation, in three self-contained
// files: Copilot's marketplace source is ./copilot and both installers fetch a
// single file from a raw URL. Nothing but this test keeps them in step.
func TestFailOpenBootstrapsShareOneImplementation(t *testing.T) {
	agents := failOpenBootstraps(t)
	reference := agents[0].shellRegion(t)
	require.NotEmpty(t, strings.TrimSpace(reference))

	for _, a := range agents[1:] {
		assert.Equal(t, reference, a.shellRegion(t),
			"%s has diverged from %s inside the shared region; apply the change to all three",
			a.Bootstrap, agents[0].Bootstrap)
	}
}

// The shared region must not name one agent, or copying it to the next one
// carries a wrong asset name that only shows up as a download 404.
func TestSharedRegionIsAgentAgnostic(t *testing.T) {
	agents := failOpenBootstraps(t)
	region := agents[0].shellRegion(t)

	for _, a := range agents {
		assert.NotContains(t, region, a.Label+"-on-event",
			"the shared region names %s; derive the name from $AGENT instead", a.Label)
	}
}

// Every bootstrap declares what the shared region consumes. A missing one is a
// `set -u` failure on the first hook event, which fail_open then swallows.
func TestBootstrapsDeclareTheSharedInputs(t *testing.T) {
	for _, a := range failOpenBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			head := strings.SplitN(a.bootstrapBody(t), sharedBegin, 2)[0]

			assert.Contains(t, head, "AGENT=\""+a.Label+"\"")
			assert.Regexp(t, `(?m)^VERSION="[0-9]+\.[0-9]+\.[0-9]+"$`, head)
			assert.Regexp(t, `(?m)^BASE=`, head)
		})
	}
}

// A downloaded binary that cannot be verified is never executed. Claude is
// included: its body differs, but the policy must not.
func TestNoBootstrapRunsAnUnverifiedBinary(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			body := a.bootstrapBody(t)

			assert.Contains(t, body, "refusing to run an unverified binary",
				"no refusal for a download with no checksums.txt entry")
			assert.Contains(t, body, "no sha256 tool",
				"no refusal for a host with no hash tool")
			assert.Contains(t, body, "checksum mismatch",
				"no refusal for a download whose digest does not match")
		})
	}
}

// TestBootstrapIsRunnable checks the one file every hook invocation goes through.
// A missing shebang or a cleared executable bit makes every event fail at the
// runtime's fork, which surfaces as silence rather than an error.
func TestBootstrapIsRunnable(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			path := abs(t, a.Bootstrap)

			assert.True(t, strings.HasPrefix(a.bootstrapBody(t), "#!"),
				"%s must start with a shebang", a.Bootstrap)
			requireExecutable(t, path, a.Bootstrap)
		})
	}
}

// downloadBlock returns the part of a bootstrap that runs when the cache is
// cold, which is the region the contract below is about.
func (a Agent) downloadBlock(t *testing.T) string {
	t.Helper()

	body := a.bootstrapBody(t)
	start := strings.Index(body, `if [ ! -x "$BINARY" ]`)
	require.NotEqual(t, -1, start,
		"%s has no cold-cache guard; update this parser", a.Bootstrap)

	end := strings.Index(body[start:], "\nfi\n")
	require.NotEqual(t, -1, end, "%s: the download block does not close", a.Bootstrap)

	// Comments use the same words the code does, so they are stripped rather than
	// matched around.
	return regexp.MustCompile(`(?m)#.*$`).ReplaceAllString(body[start:start+end], "")
}

// Every bootstrap writes the binary only by renaming a private temp over it.
//
// Hooks run concurrently and every session shares one plugin data directory, so the
// first run after a version bump has N processes finding no binary at once. Writing
// the final path directly made them interleave: against v0.1.25, 48 of 48 staggered
// invocations failed, each computing a different checksum.
//
// Static on purpose, so it holds whether or not the race reproduces here. Inside the
// download block the final path may appear only in the guard, the temp name derived
// from it, the closing rename, and a read-only -x test. The -x test is allowed
// because Windows refuses to rename over a running .exe, so a bootstrap that loses
// that race has to ask whether the winner's file is already in place.
func TestBootstrapsWriteTheBinaryOnlyByRename(t *testing.T) {
	allowed := regexp.MustCompile(
		`\[ ! -x "\$BINARY" \]|\[ -x "\$BINARY" \]|TMP="\$BINARY|mv -f "\$TMP" "\$BINARY"`)

	for _, a := range Agents {
		t.Run(a.Label, func(t *testing.T) {
			block := a.downloadBlock(t)

			for _, line := range strings.Split(block, "\n") {
				line = strings.TrimSpace(line)
				if !strings.Contains(line, `"$BINARY"`) || allowed.MatchString(line) {
					continue
				}
				t.Errorf("the download block touches $BINARY outside the guard, the temp and the rename:\n  %s", line)
			}

			assert.Contains(t, block, `mv -f "$TMP" "$BINARY"`, "no rename into place")
		})
	}
}
