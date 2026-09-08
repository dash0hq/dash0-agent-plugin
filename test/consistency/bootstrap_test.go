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

// The checks that read a .sh bootstrap as text, so they run on every platform.
// powershell_test.go is their .ps1 counterpart; the bootstrap_<goos>_test.go
// files run a bootstrap, which is what confines them to one host.

// Everything above the opening marker is agent-specific: the doc comment, AGENT,
// VERSION and the data-directory chain.
const (
	sharedBegin = "# >>> shared bootstrap"
	sharedEnd   = "# <<< shared bootstrap <<<"
)

// The marker-delimited body of a file, markers included.
func sharedRegion(t *testing.T, name, body string) string {
	t.Helper()

	start := strings.Index(body, sharedBegin)
	require.NotEqual(t, -1, start, "%s has no %q marker", name, sharedBegin)
	end := strings.Index(body, sharedEnd)
	require.NotEqual(t, -1, end, "%s has no %q marker", name, sharedEnd)
	require.Less(t, start, end, "%s has the markers in the wrong order", name)

	return body[start : end+len(sharedEnd)]
}

func (a Agent) shellRegion(t *testing.T) string {
	t.Helper()
	return sharedRegion(t, a.Bootstrap, a.bootstrapBody(t))
}

// The asset a bootstrap downloads on this host, which carries no version where the
// cached copy does. Staging a file under this name leaves the cache cold, and then
// every assertion about it passes having measured nothing.
func (a Agent) releaseAsset() string {
	return fmt.Sprintf("%s-%s-%s%s", a.AssetStem, runtime.GOOS, runtime.GOARCH, pluginrepo.ExeSuffix())
}

// The filename a bootstrap looks for under its bin directory: the asset name with
// VERSION spliced in.
func (a Agent) cacheName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%s-%s-%s-%s%s",
		a.CacheStem, a.bootstrapVersion(t), runtime.GOOS, runtime.GOARCH, pluginrepo.ExeSuffix())
}

// Every bootstrap fetches the asset name the release publishes.
//
// The fixtures elsewhere serve whatever releaseAsset() names, so nothing else here
// can see this link, and a bootstrap asking for a name goreleaser stopped building
// fails open: no binary, no telemetry, no error the user sees. Read from
// .goreleaser.yaml rather than from a release, so a rename fails at the commit
// that makes it.
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

			// The bootstrap has to ask for that stem too, or the descriptor agrees
			// with the release while the script fetches something else. The three
			// sharing a body build the name from $AGENT, so there the check is that
			// the stem is what $AGENT expands to.
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
			// The .ps1 twin hardcodes the platform, only ever running on one.
			assert.Contains(t, a.windowsBootstrapBody(t), `$Agent-on-event-windows-$Arch.exe`,
				"%s does not fetch %s-windows-<arch>.exe", a.WindowsBootstrap, a.AssetStem)
		})
	}
}

// Claude falls back to its pre-v0.1.25 asset name, which is the only one releases
// before that carry, so dropping the fallback strands a pinned older VERSION.
// Removing it is a decision, and should fail here first.
//
// The whole candidate list is parsed rather than searched for the legacy name,
// which is a suffix of the current one and so is still present as a substring with
// the fallback gone.
func TestClaudeStillFetchesItsLegacyAssetName(t *testing.T) {
	claude := agentByLabel(t, "claude")

	m := candidateList.FindStringSubmatch(claude.bootstrapBody(t))
	require.Len(t, m, 2,
		"%s has no `for CANDIDATE in ...` list; update this parser", claude.Bootstrap)

	var candidates []string
	for _, q := range quotedWord.FindAllStringSubmatch(m[1], -1) {
		candidates = append(candidates, q[1])
	}

	assert.Equal(t, []string{
		claude.AssetStem + "-${OS}-${ARCH}${EXE}",
		claude.CacheStem + "-${OS}-${ARCH}${EXE}",
	}, candidates,
		"%s must ask for %s-<os>-<arch> and then fall back to %s-<os>-<arch>, which is "+
			"the only name releases before v0.1.25 carry",
		claude.Bootstrap, claude.AssetStem, claude.CacheStem)
}

// The asset names a bootstrap tries, in order, and the quoted words in that list.
var (
	candidateList = regexp.MustCompile(`(?m)^\s*for CANDIDATE in (.+); do\s*$`)
	quotedWord    = regexp.MustCompile(`"([^"]+)"`)
)

// One implementation in three self-contained files, because Copilot's marketplace
// source is ./copilot and both installers fetch a single file from a raw URL.
// Nothing else keeps them in step.
func TestShellBootstrapsShareOneImplementation(t *testing.T) {
	agents := sharedBodyBootstraps(t)
	reference := agents[0].shellRegion(t)
	require.NotEmpty(t, strings.TrimSpace(reference))

	for _, a := range agents[1:] {
		assert.Equal(t, reference, a.shellRegion(t),
			"%s has diverged from %s inside the shared region; apply the change to all three",
			a.Bootstrap, agents[0].Bootstrap)
	}
}

// A shared region naming one agent carries a wrong asset name into the next one,
// where it shows up as a download 404 and nothing else.
func TestSharedRegionIsAgentAgnostic(t *testing.T) {
	agents := sharedBodyBootstraps(t)
	region := agents[0].shellRegion(t)

	for _, a := range agents {
		assert.NotContains(t, region, a.Label+"-on-event",
			"the shared region names %s; derive the name from $AGENT instead", a.Label)
	}
}

// A missing input is a `set -u` failure on the first hook event, which fail_open
// then swallows.
func TestBootstrapsDeclareTheSharedInputs(t *testing.T) {
	for _, a := range sharedBodyBootstraps(t) {
		t.Run(a.Label, func(t *testing.T) {
			head := strings.SplitN(a.bootstrapBody(t), sharedBegin, 2)[0]

			assert.Contains(t, head, "AGENT=\""+a.Label+"\"")
			assert.Regexp(t, `(?m)^VERSION="[0-9]+\.[0-9]+\.[0-9]+"$`, head)
			assert.Regexp(t, `(?m)^BASE=`, head)
		})
	}
}

// A downloaded binary that cannot be verified is never executed. Claude included:
// its body differs, the policy must not.
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

// A missing shebang or a cleared executable bit fails every event at the runtime's
// fork, which surfaces as silence rather than an error.
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

// The part of a bootstrap that runs when the cache is cold.
func (a Agent) downloadBlock(t *testing.T) string {
	t.Helper()

	body := a.bootstrapBody(t)
	start := strings.Index(body, `if [ ! -x "$BINARY" ]`)
	require.NotEqual(t, -1, start,
		"%s has no cold-cache guard; update this parser", a.Bootstrap)

	end := strings.Index(body[start:], "\nfi\n")
	require.NotEqual(t, -1, end, "%s: the download block does not close", a.Bootstrap)

	// Comments use the same words the code does.
	return regexp.MustCompile(`(?m)#.*$`).ReplaceAllString(body[start:start+end], "")
}

// Every bootstrap writes the binary only by renaming a private temp over it.
//
// Hooks run concurrently and every session shares one plugin data directory, so the
// first run after a version bump has N processes finding no binary at once. Writing
// the final path directly leaves them interleaving, each computing a different
// checksum. Static, so it holds whether or not the race reproduces here: inside the
// download block the final path may appear only in the guard, the temp name derived
// from it, the closing rename, and a read-only -x test. That last one is allowed
// because Windows refuses to rename over a running .exe, so a bootstrap losing the
// race has to ask whether the winner's file is already in place.
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
