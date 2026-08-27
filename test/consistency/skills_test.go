// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// frontmatter matches the YAML block a SKILL.md opens with.
var frontmatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)

// skillManifests lists every plugin manifest that declares a skills directory.
// A manifest's "skills" value resolves against base, which is the repository
// root for Claude and Cursor and the package directory for Copilot.
var skillManifests = []struct {
	label    string
	manifest string
	base     string
}{
	{"claude", ".claude-plugin/plugin.json", "."},
	{"cursor", ".cursor-plugin/plugin.json", "."},
	{"copilot", "copilot/plugin.json", "copilot"},
}

// TestSkillFrontmatterParses parses the frontmatter of every shipped skill with
// a real YAML parser.
//
// This is the check that a colon costs you. An unquoted YAML scalar ends at the
// first ": ", so a description that quotes a message containing one — such as
// "dash0: no team configured" — is not a long string, it is a parse error. The
// runtime then drops the whole skill rather than the description alone: Copilot
// reports "the following skills failed to load" in a startup line that scrolls
// past, and /dash0-configure simply does not exist. Nothing else here would
// notice, because every other check reads the file as text.
//
// A parser rather than a "no bare colon" regexp: quoting is a legitimate fix, so
// the rule is that it parses, not that it avoids a character.
func TestSkillFrontmatterParses(t *testing.T) {
	root := repoRoot(t)

	for _, m := range skillManifests {
		t.Run(m.label, func(t *testing.T) {
			declared, ok := readJSON(t, filepath.Join(root, m.manifest))["skills"].(string)
			require.True(t, ok, "%s declares no skills directory", m.manifest)

			dir := filepath.Join(root, m.base, filepath.Clean(strings.TrimPrefix(declared, "./")))
			files, err := filepath.Glob(filepath.Join(dir, "*", "SKILL.md"))
			require.NoError(t, err)
			require.NotEmpty(t, files,
				"%s declares skills at %s but ships no SKILL.md there", m.manifest, declared)

			for _, file := range files {
				name := filepath.Base(filepath.Dir(file))

				t.Run(name, func(t *testing.T) {
					body, err := os.ReadFile(file)
					require.NoError(t, err)

					match := frontmatter.FindSubmatch(body)
					require.NotNil(t, match, "%s must open with a --- frontmatter block", file)

					var fm struct {
						Name        string `yaml:"name"`
						Description string `yaml:"description"`
					}
					require.NoError(t, yaml.Unmarshal(match[1], &fm),
						"%s has unparseable frontmatter — a description holding \": \" needs quoting", file)

					// Both keys are what a runtime matches a skill on: the name is how
					// the user invokes it, the description is what the model selects it
					// by. A truncated description is the quiet half of the same bug —
					// YAML would accept "Configure the plugin" and silently drop
					// everything after the colon.
					assert.Equal(t, name, fm.Name,
						"%s declares name %q but lives in a directory named %q, so the runtime resolves neither",
						file, fm.Name, name)
					assert.Greater(t, len(fm.Description), 40,
						"%s has a %d-character description — suspiciously short, which is what a colon truncating an "+
							"unquoted scalar looks like when it happens to stay valid YAML", file, len(fm.Description))
				})
			}
		})
	}
}
