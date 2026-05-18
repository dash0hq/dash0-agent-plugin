// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package auth

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// OpenBrowser tries to open the given URL in the user's default browser.
// When DASH0_AUTH_NO_BROWSER=1 is set, it skips the launch (useful for
// tests and headless setups). Always returns nil after printing the URL
// so the caller can decide whether the absence of a browser is fatal.
func OpenBrowser(url string) error {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("DASH0_AUTH_NO_BROWSER"))); v == "1" || v == "true" {
		return nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening browser: %w", err)
	}
	// Detach so the parent can exit while the browser stays up.
	go func() { _ = cmd.Wait() }()
	return nil
}
