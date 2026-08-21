// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// write puts content in a temp file and returns its path.
func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), Name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadReadsFrontmatter(t *testing.T) {
	f := Load(write(t, `---
otlp_url: https://ingress.us1.dash0.com
auth_token: "quoted-token"
dataset: default
omit_io: false
---

# Prose after the frontmatter

otlp_url: ignored-because-it-is-not-frontmatter
`))

	assert.Equal(t, "https://ingress.us1.dash0.com", f.Get("otlp_url"))
	assert.Equal(t, "quoted-token", f.Get("auth_token"), "surrounding quotes are stripped")
	assert.Equal(t, "default", f.Get("dataset"))
	assert.Equal(t, "false", f.Get("omit_io"))
	assert.Empty(t, f.Get("agent_name"), "an absent key is empty")
}

func TestLoadStopsAtTheClosingMarker(t *testing.T) {
	f := Load(write(t, "---\ndataset: real\n---\ndataset: prose\n"))
	assert.Equal(t, "real", f.Get("dataset"))
}

func TestLoadIgnoresContentWithoutFrontmatter(t *testing.T) {
	f := Load(write(t, "dataset: never-read\n"))
	assert.Empty(t, f.Get("dataset"), "a value outside frontmatter is not configuration")
}

// A PowerShell installer writes CRLF and, with Set-Content -Encoding utf8 on
// 5.1, a BOM as well. Either one silently corrupts every value: a token gaining
// a trailing CR fails to authenticate, and nothing reports why.
func TestLoadToleratesCRLFAndBOM(t *testing.T) {
	f := Load(write(t, "\ufeff---\r\notlp_url: https://ingress.us1.dash0.com\r\nauth_token: tok\r\n---\r\n"))

	assert.Equal(t, "https://ingress.us1.dash0.com", f.Get("otlp_url"))
	assert.Equal(t, "tok", f.Get("auth_token"))
}

func TestLoadTakesTheFirstExistingPath(t *testing.T) {
	project := write(t, "---\ndataset: project\n---\n")
	global := write(t, "---\ndataset: global\nteam_name: only-in-global\n---\n")

	f := Load(project, global)
	assert.Equal(t, project, f.Path)
	assert.Equal(t, "project", f.Get("dataset"))
	assert.Empty(t, f.Get("team_name"),
		"the winning file is used whole; keys are not merged from the next one")

	assert.Equal(t, "global", Load(filepath.Join(t.TempDir(), "absent.md"), global).Get("dataset"))
}

func TestLoadWithNoFileIsUsableAndEnabled(t *testing.T) {
	f := Load(filepath.Join(t.TempDir(), "absent.md"))

	assert.Empty(t, f.Path)
	assert.Empty(t, f.Get("otlp_url"))
	assert.True(t, f.Enabled(), "an unconfigured install is not a disabled one")
}

func TestEnabled(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"absent key", "---\ndataset: d\n---\n", true},
		{"explicit false", "---\nenabled: false\n---\n", false},
		{"quoted false", "---\nenabled: \"false\"\n---\n", false},
		{"explicit true", "---\nenabled: true\n---\n", true},
		{"anything else", "---\nenabled: maybe\n---\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Load(write(t, tt.content)).Enabled())
		})
	}
}

func TestParseEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		content string
		key     string
		want    string
	}{
		{"no space after colon", "---\ndataset:tight\n---", "dataset", "tight"},
		{"extra spaces are trimmed", "---\ndataset:    padded   \n---", "dataset", "padded"},
		{"value containing a colon", "---\notlp_url: https://host:4318\n---", "otlp_url", "https://host:4318"},
		{"first occurrence wins", "---\ndataset: one\ndataset: two\n---", "dataset", "one"},
		{"line without a colon is skipped", "---\nnonsense\ndataset: d\n---", "dataset", "d"},
		{"empty value", "---\ndataset:\n---", "dataset", ""},
		{"empty file", "", "dataset", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Load(write(t, tt.content)).Get(tt.key))
		})
	}
}
