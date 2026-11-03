// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The test binary doubles as a controlled Amp executable. This exercises the
// real exec path on all supported OSes without launching a paid agent run.
func TestMain(m *testing.M) {
	if mode := os.Getenv("DASH0_TEST_AMP_EXPORT"); mode != "" {
		if strings.Join(os.Args[1:], " ") != "threads export T-test" {
			os.Exit(4)
		}
		switch mode {
		case "ok":
			fmt.Print(exportFixture)
		case "malformed":
			fmt.Print(`{"messages":`)
		case "error":
			fmt.Fprint(os.Stderr, "SECRET")
			os.Exit(3)
		case "large":
			fmt.Print(strings.Repeat("x", 17<<20))
		case "hang":
			time.Sleep(time.Minute)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestExportSubprocessBoundaries(t *testing.T) {
	path, err := os.Executable()
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	dir := t.TempDir()
	name := "amp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0700))
	t.Setenv("PATH", dir)
	// Only "hang" is about the timeout firing; the others just have to finish.
	// Giving them a generous budget keeps a slow, race-instrumented subprocess
	// on a loaded machine from being killed and read as a product failure.
	original := exportTimeout
	t.Cleanup(func() { exportTimeout = original })
	for _, mode := range []string{"ok", "error", "large", "hang"} {
		t.Run(mode, func(t *testing.T) {
			exportTimeout = original
			if mode != "hang" {
				exportTimeout = time.Minute
			}
			t.Setenv("DASH0_TEST_AMP_EXPORT", mode)
			start := time.Now()
			out, err := exportThread("T-test")
			if mode == "ok" {
				require.NoError(t, err)
				require.JSONEq(t, exportFixture, string(out))
			} else {
				require.Error(t, err)
				require.Nil(t, out)
				require.NotContains(t, err.Error(), "SECRET")
			}
			require.Less(t, time.Since(start), 10*time.Second)
		})
	}
}

func TestMissingAmpDoesNotReturnUsage(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	out, err := exportThread("T-test")
	require.Error(t, err)
	require.Nil(t, out)
}
