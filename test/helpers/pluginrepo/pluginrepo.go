// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package pluginrepo answers questions about the checkout under test: where its
// root is, what release each bootstrap pins, what the built artifact is called on
// this platform, and how to put a piece of it somewhere a test can run it.
package pluginrepo

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Root is the module root. Tests resolve every repo-relative path against
// it, so they can run from any package directory.
//
// One value for every runtime. What varies per runtime is the plugin root a
// manifest's own paths resolve against, which is "." for three of the four and
// "copilot" for Copilot; that lives on Agent.PluginRoot in test/consistency.
func Root(t *testing.T) string {
	t.Helper()
	dir, err := FindRoot()
	require.NoError(t, err)
	return dir
}

// FindRoot walks up to the module root, identified by go.mod. Separate from
// Root so a TestMain, which has no *testing.T, can locate it.
//
// Anchored on this file's own path rather than the working directory, because a
// test that has moved cwd still has to resolve the checkout. go.mod is the
// marker because there is one per module, where a runtime's manifest is one of
// four and can be renamed.
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

// BuildBinary compiles one entrypoint into a temp dir and returns its path.
// Pass the package path, e.g. "./cmd/claude-on-event".
//
// The .exe is not cosmetic: go build honors -o verbatim, and on Windows a file
// written without it cannot be run. The failure reads "executable file not found
// in %PATH%" from a path that plainly exists.
func BuildBinary(t *testing.T, root, pkg string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(pkg)+ExeSuffix())
	build := exec.Command("go", "build", "-o", bin, pkg)
	build.Dir = root
	out, err := build.CombinedOutput()
	require.NoError(t, err, "building %s: %s", pkg, out)
	return bin
}

var versionPin = regexp.MustCompile(`(?m)^VERSION="([^"]+)"`)

// BootstrapVersion reads the release a bootstrap pins. A test that pre-stages a
// binary must name it with this version, or the script will not find it and will
// fall back to downloading from the release.
// Pass the repo-relative script, e.g. "claude/claude-on-event.sh".
func BootstrapVersion(t *testing.T, root, script string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, script))
	require.NoError(t, err)
	m := versionPin.FindSubmatch(body)
	require.NotNil(t, m, `no VERSION="..." in %s`, script)
	return string(m[1])
}

// OSArch mirrors the platform detection in the bootstraps and installers, so a
// pre-staged binary's name matches what they derive from uname.
func OSArch(t *testing.T) (string, string) {
	t.Helper()

	// Windows is answered from Go: a Go test runs natively, so there may be no
	// uname at all, and where there is one it reports MINGW64_NT-10.0-26200,
	// which the bootstraps then map back to "windows" anyway.
	if runtime.GOOS == "windows" {
		return runtime.GOOS, runtime.GOARCH
	}

	osOut, err := exec.Command("uname", "-s").Output()
	require.NoError(t, err)
	archOut, err := exec.Command("uname", "-m").Output()
	require.NoError(t, err)
	goos := strings.ToLower(strings.TrimSpace(string(osOut)))
	arch := strings.TrimSpace(string(archOut))
	switch arch {
	case "x86_64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	}
	return goos, arch
}

// ExeSuffix is what GoReleaser appends to a Windows build, and what both bootstraps
// carry through to the release asset name and the cached filename.
func ExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// CachedBinary is the filename a bootstrap looks for in its bin directory. Pass the
// stem it builds the name from ("codex-on-event", or plain "on-event" for Claude)
// and the version it pins.
//
// A name wrong by one character defeats the staging silently: the bootstrap fetches
// the last release and the canary reports green against shipped code. Hence one
// place rather than a format string at each call site.
func CachedBinary(t *testing.T, stem, version string) string {
	t.Helper()
	goos, arch := OSArch(t)
	return fmt.Sprintf("%s-%s-%s-%s%s", stem, version, goos, arch, ExeSuffix())
}

// CopyDir recursively copies src to dst, preserving modes.
func CopyDir(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	}))
}

// CopyExecutable copies src to dst with the executable bit set.
func CopyExecutable(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o755))
}

// StageSkill writes a project-scoped skill into dir and returns its name.
//
// Claude Code discovers .claude/skills/<name>/SKILL.md and, per `copilot skill
// --help`, so does Copilot, so one staged skill drives both. Codex has no skill
// concept, which is why its test asserts no skill invocation.
//
// The skill exists to be invoked, not to do work. Its body asks for one literal
// token, so the turn stays cheap.
func StageSkill(t *testing.T, dir string) string {
	t.Helper()

	const name = "dash0-e2e-probe"
	skillDir := filepath.Join(dir, ".claude", "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755), "creating %s", skillDir)

	body := fmt.Sprintf(`---
name: %s
description: Emit a fixed acknowledgement token. Use this skill whenever the user asks you to run the %s skill, or asks for the probe token.
---

# Probe

Reply with exactly this text and nothing else:

DASH0_SKILL_OK
`, name, name)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644),
		"writing SKILL.md")
	return name
}
