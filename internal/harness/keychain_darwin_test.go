// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package harness

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realHome is the developer's home directory, captured at package
// initialization — which runs before TestMain repoints HOME at a temp
// directory.
//
// `security` locates the login keychain through HOME, so every lookup made
// under that temp home fails with SecKeychainSearchCopyNext and finds nothing.
// The tests below restore this value for their duration. It is also worth
// knowing in production: an agent that runs with a rewritten HOME gets no
// keychain token and no error either.
var realHome = os.Getenv("HOME")

// keychainService returns a service name unique to the test and the process, so
// concurrent runs cannot collide and a leaked item cannot be mistaken for a
// fresh one.
func keychainService(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("dash0-agent-plugin-test-%s-%d", t.Name(), os.Getpid())
}

// addKeychainItem stores a generic password in the login keychain and deletes it
// when the test ends. The account is required: the CLI rejects the call without
// one, with a usage error rather than anything keychain-specific.
//
// -A grants access to every application, which is what keeps the read from
// blocking on an interactive "security wants to use your confidential
// information" prompt. That makes this shape test-only: a real item should be
// provisioned with a narrower ACL.
//
// A keychain that cannot be written is a plausible local state — locked, or
// absent in a headless session — so the test skips. In CI it fails instead: a
// silently skipped test reports green while verifying nothing.
func addKeychainItem(t *testing.T, service, account, token string) {
	t.Helper()

	args := []string{"add-generic-password", "-s", service, "-a", account, "-w", token, "-A", "-U"}
	if out, err := exec.Command("security", args...).CombinedOutput(); err != nil {
		msg := fmt.Sprintf("cannot write to the login keychain: %v: %s", err, out)
		if os.Getenv("CI") != "" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}

	t.Cleanup(func() {
		_ = exec.Command("security", "delete-generic-password", "-s", service, "-a", account).Run()
	})
}

func TestKeychainTokenReadsAStoredItem(t *testing.T) {
	t.Setenv("HOME", realHome)
	service := keychainService(t)
	addKeychainItem(t, service, "dash0", "token-from-keychain")

	assert.Equal(t, "token-from-keychain", keychainToken(service, "dash0"))
}

// AUTH_TOKEN_KEYCHAIN_ACCOUNT is documented as optional, so a config naming only
// the service has to match. The item itself always carries an account, because
// `security add-generic-password` requires one — what varies is whether the
// lookup names it.
func TestKeychainTokenWithoutAnAccountInTheLookup(t *testing.T) {
	t.Setenv("HOME", realHome)
	service := keychainService(t)
	addKeychainItem(t, service, "provisioned-by-mdm", "token-without-account")

	assert.Equal(t, "token-without-account", keychainToken(service, ""))
}

func TestKeychainTokenMissReturnsEmpty(t *testing.T) {
	t.Setenv("HOME", realHome)

	assert.Empty(t, keychainToken(keychainService(t)+"-absent", ""),
		"a missing item is a configuration state, not an error")
}

// The end-to-end contract plugin.json and the Claude README promise: a
// successful lookup takes precedence over AUTH_TOKEN, whether that token came
// from the environment or the configuration file. This is what lets a managed
// rollout ship a pointer instead of the secret.
func TestKeychainTokenOutranksTheEnvironmentAndTheFile(t *testing.T) {
	t.Setenv("HOME", realHome)
	service := keychainService(t)
	addKeychainItem(t, service, "dash0", "token-from-keychain")

	// A project file beats the one in the home directory, so pointing the working
	// directory at a fresh project keeps the developer's real config out of this.
	t.Chdir(writeConfig(t, t.TempDir(), Claude, fmt.Sprintf(
		"---\nauth_token: token-from-file\nauth_token_keychain_service: %s\nauth_token_keychain_account: dash0\n---\n",
		service)))
	t.Setenv("CLAUDE_PLUGIN_OPTION_AUTH_TOKEN", "token-from-option")

	assert.Equal(t, "token-from-keychain", Claude.Config().AuthToken)
}

// A keychain reference that resolves to nothing must fall through rather than
// blank the token, so a fleet where provisioning has not run yet still reports.
func TestKeychainMissFallsThroughToTheEnvironment(t *testing.T) {
	t.Setenv("HOME", realHome)
	t.Chdir(writeConfig(t, t.TempDir(), Claude, fmt.Sprintf(
		"---\nauth_token_keychain_service: %s-absent\n---\n", keychainService(t))))
	t.Setenv("CLAUDE_PLUGIN_OPTION_AUTH_TOKEN", "token-from-option")

	require.Equal(t, "token-from-option", Claude.Config().AuthToken)
}
