// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"os/exec"
	"strings"
)

// keychainToken reads a generic password from the macOS keychain.
//
// This lets a managed or MDM rollout ship only a pointer — safe to place in
// managed-settings.json — while the secret is provisioned separately with
// `security add-generic-password -s <service> -a <account> -w <token>`.
//
// Every failure is silent and returns empty, so the caller falls through to the
// next source: a keychain that holds no such item is a configuration state, not
// an error worth breaking a session over. An empty account means the service
// alone identifies the item.
func keychainToken(service, account string) string {
	args := []string{"find-generic-password", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	args = append(args, "-w")

	out, err := exec.Command("security", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
