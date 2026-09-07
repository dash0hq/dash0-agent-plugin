// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package pluginrepo answers questions about the checkout under test: where its
// root is, and what a built artifact is called on this platform.
package pluginrepo

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// The module root, which every repo-relative path resolves against so a test can
// run from any package directory. One value for every runtime: what varies is the
// plugin root a manifest's own paths resolve against, and that lives on
// Agent.PluginRoot in test/consistency.
func Root(t *testing.T) string {
	t.Helper()
	dir, err := FindRoot()
	require.NoError(t, err)
	return dir
}

// Separate from Root so a caller with no *testing.T can locate it too.
//
// Anchored on this file's own path rather than the working directory, a test that
// moved cwd still having to resolve the checkout. go.mod is the marker because
// there is one per module, where a runtime's manifest is one of four.
func FindRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("could not resolve this file's path to start the module-root walk")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find the module root (no go.mod) above %s", dir)
		}
		dir = parent
	}
}

// What GoReleaser appends to a Windows build, and what the bootstraps carry through
// to the release asset name and the cached filename.
func ExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
