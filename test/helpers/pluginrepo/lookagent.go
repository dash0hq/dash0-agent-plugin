// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginrepo

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// LookAgent resolves an agent CLI on PATH, skipping the plugin's own launch
// wrappers.
//
// dash0-configure installs a wrapper, `copilot.cmd` in ~/.local/bin and the
// equivalent for the other runtimes, that forces the native-OTel exporter to a
// private file it deletes on exit. A test that runs the wrapper gets no native-OTel
// file at all. Git Bash never picks it, but exec.LookPath does via PATHEXT, so this
// broke only on a Windows machine with the plugin installed.
//
// PATH is walked directly because LookPath returns only the first hit.
func LookAgent(t *testing.T, name string) (string, error) {
	t.Helper()

	exts := []string{""}
	if runtime.GOOS == "windows" {
		// PATHEXT only. The extensionless launcher npm also installs is a POSIX
		// shell script CreateProcess cannot run.
		pathext := os.Getenv("PATHEXT")
		if pathext == "" {
			pathext = ".COM;.EXE;.BAT;.CMD"
		}
		exts = strings.Split(strings.ToLower(pathext), ";")
	}

	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			p := filepath.Join(dir, name+ext)
			info, err := os.Stat(p)
			if err != nil || info.IsDir() {
				continue
			}
			// Windows decides by extension and reports 0o666 for every file, so the
			// bit only means something on POSIX.
			if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
				continue
			}
			if body, err := os.ReadFile(p); err == nil &&
				strings.Contains(string(body), "dash0-agent-plugin") {
				continue // our launch wrapper, not the CLI
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("%s CLI not found on PATH", name)
}
