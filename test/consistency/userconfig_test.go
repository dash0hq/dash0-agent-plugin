// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/test/helpers/pluginrepo"
)

// Option keys the Go code reads but the manifest deliberately does not declare.
// Both are development switches, not user-facing configuration.
var devOnlyOptions = map[string]bool{"DEBUG": true, "DEBUG_FILE": true}

// optionSite records where an option key is read, so a failure names a file
// rather than leaving you to grep for it.
type optionSite struct {
	file string
	line int
}

func (s optionSite) String() string { return fmt.Sprintf("%s:%d", s.file, s.line) }

// discoverOptionKeys returns every option key the Go code reads, mapped to its
// first call site.
//
// It walks the syntax tree rather than grepping. The shell version greps two
// hardcoded files for the accessor names, and went blind twice when the code
// moved. Walking removes the file list; the anchors in
// TestClaudeUserConfigCoversEveryOptionRead cover a rename of the accessors.
//
// Only constant string arguments count. An accessor called with a variable key
// contributes nothing, which is correct: such a call site declares no key.
func discoverOptionKeys(t *testing.T) map[string]optionSite {
	t.Helper()

	root := pluginrepo.Root(t)
	found := map[string]optionSite{}
	fset := token.NewFileSet()

	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Test files name fake keys, which are not what ships.
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			require.NoError(t, parseErr, "parsing %s", path)

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				// The accessors exist in both cases, because some are unexported.
				if !ok || !strings.HasPrefix(strings.ToLower(sel.Sel.Name), "pluginoption") {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				key, unquoteErr := strconv.Unquote(lit.Value)
				require.NoError(t, unquoteErr)

				if _, seen := found[key]; !seen {
					rel, relErr := filepath.Rel(root, path)
					require.NoError(t, relErr)
					found[key] = optionSite{file: rel, line: fset.Position(lit.Pos()).Line}
				}
				return true
			})
			return nil
		})
		require.NoError(t, err, "walking %s", dir)
	}
	return found
}

// userConfigAgent is the runtime whose manifest declares userConfig. Options are
// named per runtime with a different env prefix but the same suffix, so this one
// manifest is where every option gets declared and documented.
func userConfigAgent(t *testing.T) Agent {
	t.Helper()
	return agentsWith(t, 1, func(a Agent) bool { return a.ManifestUserConfig })[0]
}

func (a Agent) declaredOptions(t *testing.T) []string {
	t.Helper()
	declared, ok := a.manifest(t)["userConfig"].(map[string]any)
	require.True(t, ok, "%s declares no userConfig", a.Manifest)

	keys := make([]string, 0, len(declared))
	for k := range declared {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestClaudeUserConfigCoversEveryOptionRead ties the declared options to the
// code that reads them, in both directions. A key read but not declared is
// invisible to the user; a key declared but not read does nothing when set.
func TestClaudeUserConfigCoversEveryOptionRead(t *testing.T) {
	a := userConfigAgent(t)
	declared := a.declaredOptions(t)
	used := discoverOptionKeys(t)

	// Anchors. If the accessors are renamed, discovery quietly returns nothing
	// and every assertion below would pass on empty sets. These two fail first,
	// and say why.
	require.Contains(t, used, "OTLP_URL",
		"option discovery found no OTLP_URL; the accessor names in internal/harness changed, update discoverOptionKeys")
	require.Contains(t, used, "AUTH_TOKEN",
		"option discovery found no AUTH_TOKEN; the accessor names in internal/harness changed, update discoverOptionKeys")

	// Every declared option, with no exception. The two keychain options used to
	// be one: only the bootstrap read them, so the check skipped them. The binary
	// reads them now (internal/harness resolves the keychain), so they are
	// ordinary options and an exception would hide a real break.
	for _, key := range declared {
		assert.Contains(t, used, key,
			"%s declares %s but no Go code reads it", a.Manifest, key)
	}

	for key, site := range used {
		if devOnlyOptions[key] {
			continue
		}
		assert.Contains(t, declared, key,
			"%s reads option %s, which %s does not declare; declare it or add it to devOnlyOptions",
			site, key, a.Manifest)
	}
}

// TestClaudeUserConfigIsDocumented keeps the README tables in step with the
// manifest. Every declared option must appear as a table row, so the
// Configuration and Privacy sections cannot silently fall behind.
func TestClaudeUserConfigIsDocumented(t *testing.T) {
	a := agentsWith(t, 1, func(a Agent) bool { return a.OptionDocs != "" })[0]

	docs, err := os.ReadFile(abs(t, a.OptionDocs))
	require.NoError(t, err)

	for _, key := range a.declaredOptions(t) {
		assert.Contains(t, string(docs), "| `"+key+"`",
			"%s declares %s but %s has no table row for it", a.Manifest, key, a.OptionDocs)
	}
}

// The one option that must never be written in the clear.
//
// `sensitive: true` is what makes `claude plugin install --config` put AUTH_TOKEN
// in the secrets store rather than in settings.json, which fleet admins commit.
// Flipping it to false puts a customer's ingest token in a tracked file, and
// every other check here passes: TestClaudeUserConfigCoversEveryOptionRead reads
// the key set and TestClaudeUserConfigIsDocumented reads the descriptions, so
// neither looks at this flag.
//
// test/contracts/claude.sh used to assert the effect — that the token lands in
// .credentials.json and not in settings.json. That contract needed the Claude CLI
// and a Linux host, and it went with the shell tree. This asserts the input to it
// instead, which is the half a laptop can check.
//
// Every other option is asserted false in the same pass, so a new one cannot
// arrive quietly marked sensitive and hide a real secret among the mundane.
func TestClaudeUserConfigMarksOnlyTheTokenSensitive(t *testing.T) {
	a := userConfigAgent(t)
	declared, ok := a.manifest(t)["userConfig"].(map[string]any)
	require.True(t, ok, "%s declares no userConfig", a.Manifest)

	for _, key := range a.declaredOptions(t) {
		option, ok := declared[key].(map[string]any)
		require.True(t, ok, "%s: %s must be an object", a.Manifest, key)

		// Present on every option, not defaulted: a missing key reads as false,
		// which is the wrong answer for exactly the one that matters.
		sensitive, ok := option["sensitive"].(bool)
		require.True(t, ok, "%s: %s declares no boolean \"sensitive\"", a.Manifest, key)

		assert.Equal(t, key == "AUTH_TOKEN", sensitive,
			"%s: %s has sensitive=%v; only AUTH_TOKEN holds a secret, and it is the "+
				"flag that keeps it out of settings.json", a.Manifest, key, sensitive)
	}
}
