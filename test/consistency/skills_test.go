// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package consistency

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// frontmatter matches the YAML block a SKILL.md opens with.
var frontmatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)

// skillFiles returns every SKILL.md a runtime ships, and fails when a runtime
// that declares a skills directory ships none. The directory comes from the
// descriptor rather than being re-read here: manifest_test.go already pins the
// manifest's own "skills" value against it.
func (a Agent) skillFiles(t *testing.T) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(a.pkgPath(t, a.ManifestSkills), "*", "SKILL.md"))
	require.NoError(t, err)
	require.NotEmpty(t, files,
		"%s declares skills at %s but ships no SKILL.md there", a.Manifest, a.ManifestSkills)
	return files
}

// TestSkillFrontmatterParses parses the frontmatter of every shipped skill with
// a real YAML parser.
//
// An unquoted YAML scalar ends at the first ": ", so a description quoting a
// message such as "dash0: no team configured" is a parse error rather than a long
// string. The runtime then drops the whole skill: Copilot reports "the following
// skills failed to load" in a startup line that scrolls past, and
// /dash0-configure does not exist. Every other check here reads the file as text
// and would miss it.
//
// A parser rather than a "no bare colon" regexp, because quoting is a legitimate
// fix. The rule is that it parses, not that it avoids a character.
func TestSkillFrontmatterParses(t *testing.T) {
	for _, a := range agentsWith(t, 3, func(a Agent) bool { return a.ManifestSkills != "" }) {
		t.Run(a.Label, func(t *testing.T) {
			for _, file := range a.skillFiles(t) {
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

					// A runtime matches on both keys: the name is how the user invokes
					// the skill, the description is what the model selects it by. YAML
					// would accept "Configure the plugin" and drop everything after
					// the colon.
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
