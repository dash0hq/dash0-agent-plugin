// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/amp"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/pi"
)

// Amp's installers fetch amp/index.ts as a single file, so the Amp and Pi
// bridges cannot import a shared module. Their content helpers are copies
// instead, and this keeps the copies identical: a privacy fix in one, such as
// a new rule for which blocks may enter telemetry, must reach the other.
func TestBridgeContentHelpersAreIdentical(t *testing.T) {
	root := repoRoot(t)
	block := func(path string) string {
		t.Helper()
		_, rest, ok := strings.Cut(readBridge(t, root, path), "// BEGIN content helpers")
		require.True(t, ok, "%s has no BEGIN content helpers marker", path)
		body, _, ok := strings.Cut(rest, "// END content helpers")
		require.True(t, ok, "%s has no END content helpers marker", path)
		return body
	}
	amp := block("amp/index.ts")
	require.Contains(t, amp, "function asText", "the block holds the helpers")
	require.Contains(t, amp, "function messageText", "the block holds the helpers")
	require.Equal(t, amp, block("pi/index.ts"),
		"amp/index.ts and pi/index.ts must carry the same content helpers")
}

// The bridges budget content against limits that the Go side enforces. The
// field cap equals otlp.MaxContentBytes: a larger cap keeps text that the
// exporter cuts anyway, and a smaller one cuts text that the exporter keeps.
// An envelope cap at or above the helper's own limit makes that helper reject
// whole turns.
func TestBridgeBudgetsFitTheHelpers(t *testing.T) {
	root := repoRoot(t)
	limit := func(src, name string) int {
		t.Helper()
		// Either `N * 1024` or a plain number of bytes. Other forms, such as
		// `1 << 20`, `16_384` or a trailing comment, do not match.
		m := regexp.MustCompile(`(?m)^const ` + name + ` = (\d+)( \* 1024)?;$`).FindStringSubmatch(src)
		require.NotNil(t, m, "no %s in the bridge; write it as `const %s = N;` or `const %s = N * 1024;`", name, name, name)
		n, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		if m[2] != "" {
			n *= 1024
		}
		return n
	}
	for path, helperLimit := range map[string]int{
		"amp/index.ts": amp.MaxEnvelopeBytes,
		"pi/index.ts":  pi.MaxEnvelopeBytes,
	} {
		src := readBridge(t, root, path)
		require.Equal(t, otlp.MaxContentBytes, limit(src, "maxField"), path)
		require.Less(t, limit(src, "maxEnvelope"), helperLimit, path)
		// A sanity bound, not a guarantee: JSON escaping can grow content
		// up to six times (a control character becomes `\u00XX`), and the
		// rest of the envelope needs room too. The bridges shed content at
		// run time when the encoded envelope still passes maxEnvelope.
		require.LessOrEqual(t, 2*limit(src, "maxContent"), limit(src, "maxEnvelope"), path)
	}
}

// readBridge reads a bridge source with CRLF line ends made LF, so that the
// block markers and the `;$` anchor match on a Windows checkout too.
func readBridge(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path))
	require.NoError(t, err)
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}
