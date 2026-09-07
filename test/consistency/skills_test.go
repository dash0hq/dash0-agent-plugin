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

// The YAML block a SKILL.md opens with.
var frontmatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)

// Every SKILL.md a runtime ships. The directory comes from the descriptor, which
// TestManifestDeclaresSkills holds against the manifest's own value.
func (a Agent) skillFiles(t *testing.T) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(a.pkgPath(t, a.ManifestSkills), "*", "SKILL.md"))
	require.NoError(t, err)
	require.NotEmpty(t, files,
		"%s declares skills at %s but ships no SKILL.md there", a.Manifest, a.ManifestSkills)
	return files
}

// An unquoted YAML scalar ends at the first ": ", so a description quoting a
// message such as "dash0: no team configured" is a parse error rather than a long
// string, and the runtime drops the whole skill: Copilot says "the following skills
// failed to load" in a startup line that scrolls past, and /dash0-configure does
// not exist. A parser rather than a "no bare colon" regexp, because quoting is a
// legitimate fix.
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

					// Both keys are load-bearing: the name is how the user invokes the
					// skill, the description what the model selects it by.
					assert.Equal(t, name, fm.Name,
						"%s declares name %q but lives in a directory named %q, so the runtime resolves neither",
						file, fm.Name, name)
					assert.Greater(t, len(fm.Description), 40,
						"%s has a %d-character description, which is what a colon truncating an "+
							"unquoted scalar leaves behind when it stays valid YAML", file, len(fm.Description))
				})
			}
		})
	}
}
