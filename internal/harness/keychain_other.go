// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package harness

// keychainToken has no implementation outside macOS. The keychain options exist
// for managed macOS fleets; the Windows equivalent would be Credential Manager,
// which is not wired up. Returning empty makes the caller fall through to the
// configuration file and the environment, so a config that names a keychain
// service on Linux or Windows is inert rather than broken.
func keychainToken(_, _ string) string {
	return ""
}
