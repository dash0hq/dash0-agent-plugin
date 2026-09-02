// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginrepo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStageSkill checks the staged skill lands where both runtimes look for it.
func TestStageSkill(t *testing.T) {
	dir := t.TempDir()
	name := StageSkill(t, dir)

	body, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", name, "SKILL.md"))
	require.NoError(t, err, "skill must land in the project-scoped path both runtimes discover")
	assert.Contains(t, string(body), "name: "+name, "frontmatter must declare the skill name")
	assert.Contains(t, string(body), "DASH0_SKILL_OK")
}
