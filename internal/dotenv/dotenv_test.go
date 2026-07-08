// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package dotenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoad(t *testing.T) {
	t.Run("sets variables from file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "KEY_A=hello\nKEY_B=world")
		t.Setenv("KEY_A", "")
		os.Unsetenv("KEY_A")
		os.Unsetenv("KEY_B")

		Load(filepath.Join(dir, ".env"))

		assert.Equal(t, "hello", os.Getenv("KEY_A"))
		assert.Equal(t, "world", os.Getenv("KEY_B"))
		os.Unsetenv("KEY_A")
		os.Unsetenv("KEY_B")
	})

	t.Run("does not overwrite existing env vars", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "EXISTING=from-file")
		t.Setenv("EXISTING", "from-env")

		Load(filepath.Join(dir, ".env"))

		assert.Equal(t, "from-env", os.Getenv("EXISTING"))
	})

	t.Run("skips comments and blank lines", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "# comment\n\n  \nVALID=yes")
		os.Unsetenv("VALID")

		Load(filepath.Join(dir, ".env"))

		assert.Equal(t, "yes", os.Getenv("VALID"))
		os.Unsetenv("VALID")
	})

	t.Run("strips double quotes", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, `QUOTED="hello world"`)
		os.Unsetenv("QUOTED")

		Load(filepath.Join(dir, ".env"))

		assert.Equal(t, "hello world", os.Getenv("QUOTED"))
		os.Unsetenv("QUOTED")
	})

	t.Run("strips single quotes", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, `SINGLE='hello world'`)
		os.Unsetenv("SINGLE")

		Load(filepath.Join(dir, ".env"))

		assert.Equal(t, "hello world", os.Getenv("SINGLE"))
		os.Unsetenv("SINGLE")
	})

	t.Run("skips lines without equals sign", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "NOEQUALSSIGN\nGOOD=yes")
		os.Unsetenv("GOOD")

		Load(filepath.Join(dir, ".env"))

		assert.Equal(t, "yes", os.Getenv("GOOD"))
		os.Unsetenv("GOOD")
	})

	t.Run("missing file is a no-op", func(t *testing.T) {
		Load("/nonexistent/path/.env")
	})

	t.Run("trims whitespace around key and value", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "  SPACED  =  value  ")
		os.Unsetenv("SPACED")

		Load(filepath.Join(dir, ".env"))

		assert.Equal(t, "value", os.Getenv("SPACED"))
		os.Unsetenv("SPACED")
	})
}

func writeFile(t *testing.T, dir, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o644)
	if err != nil {
		t.Fatal(err)
	}
}
