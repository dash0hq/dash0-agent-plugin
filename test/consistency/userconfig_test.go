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

// Read by the Go code, deliberately not declared: development switches, not
// user-facing configuration.
var devOnlyOptions = map[string]bool{"DEBUG": true, "DEBUG_FILE": true}

// Where an option key is read, so a failure names a file.
type optionSite struct {
	file string
	line int
}

func (s optionSite) String() string { return fmt.Sprintf("%s:%d", s.file, s.line) }

// Every option key the Go code reads, mapped to its first call site.
//
// The syntax tree rather than a grep, so moving a call site does not silently
// drop it from the results; a rename of the accessors is covered by the anchors
// in TestClaudeUserConfigCoversEveryOptionRead. Only constant string arguments
// count, a call with a variable key declaring no key at all.
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

// The runtime whose manifest declares userConfig. The env prefix differs per
// runtime but the suffix does not, so this one manifest declares and documents
// every option.
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

// The declared options against the code that reads them, in both directions. A key
// read but not declared is invisible to the user; a key declared but not read does
// nothing when set.
func TestClaudeUserConfigCoversEveryOptionRead(t *testing.T) {
	a := userConfigAgent(t)
	declared := a.declaredOptions(t)
	used := discoverOptionKeys(t)

	// Renamed accessors leave discovery returning nothing, and every assertion
	// below then passes on empty sets. These two fail first, and say why.
	require.Contains(t, used, "OTLP_URL",
		"option discovery found no OTLP_URL; the accessor names in internal/harness changed, update discoverOptionKeys")
	require.Contains(t, used, "AUTH_TOKEN",
		"option discovery found no AUTH_TOKEN; the accessor names in internal/harness changed, update discoverOptionKeys")

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

// Every declared option appears as a README table row, so the Configuration and
// Privacy sections cannot silently fall behind the manifest.
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
// Flipping it to false puts a customer's ingest token in a tracked file, and no
// other check here reads the flag. Every other option is asserted false in the
// same pass, so a new secret cannot arrive hidden among the mundane.
func TestClaudeUserConfigMarksOnlyTheTokenSensitive(t *testing.T) {
	a := userConfigAgent(t)
	declared, ok := a.manifest(t)["userConfig"].(map[string]any)
	require.True(t, ok, "%s declares no userConfig", a.Manifest)

	for _, key := range a.declaredOptions(t) {
		option, ok := declared[key].(map[string]any)
		require.True(t, ok, "%s: %s must be an object", a.Manifest, key)

		// Required, not defaulted: a missing key reads as false, which is the wrong
		// answer for exactly the one that matters.
		sensitive, ok := option["sensitive"].(bool)
		require.True(t, ok, "%s: %s declares no boolean \"sensitive\"", a.Manifest, key)

		assert.Equal(t, key == "AUTH_TOKEN", sensitive,
			"%s: %s has sensitive=%v; only AUTH_TOKEN holds a secret, and it is the "+
				"flag that keeps it out of settings.json", a.Manifest, key, sensitive)
	}
}
