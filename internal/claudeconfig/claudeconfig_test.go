// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package claudeconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude Code resolves its config file through two candidates, not one, and the
// fallbacks are asymmetric — the .config.json probe is rooted at ~/.claude while
// the .claude.json fallback is rooted at $HOME itself. Getting that backwards
// silently reads nothing. Mirrors CLI 2.1.81:
//
//	configDir = $CLAUDE_CONFIG_DIR ?? join(home, ".claude")
//	exists(configDir/.config.json) ? configDir/.config.json
//	                               : join($CLAUDE_CONFIG_DIR || home, ".claude.json")
func TestConfigPath(t *testing.T) {
	t.Run("no override, no .config.json: $HOME/.claude.json", func(t *testing.T) {
		home := t.TempDir()
		assert.Equal(t, filepath.Join(home, ".claude.json"), configPath(home, ""))
	})

	t.Run("no override, ~/.claude/.config.json wins when present", func(t *testing.T) {
		home := t.TempDir()
		cfgDir := filepath.Join(home, ".claude")
		require.NoError(t, os.MkdirAll(cfgDir, 0o755))
		writeFile(t, filepath.Join(cfgDir, ".config.json"), `{}`)

		assert.Equal(t, filepath.Join(cfgDir, ".config.json"), configPath(home, ""))
	})

	t.Run("CLAUDE_CONFIG_DIR set, no .config.json: <dir>/.claude.json", func(t *testing.T) {
		home, dir := t.TempDir(), t.TempDir()
		// NOT $HOME/.claude.json — the override reroots the fallback too.
		assert.Equal(t, filepath.Join(dir, ".claude.json"), configPath(home, dir))
	})

	t.Run("CLAUDE_CONFIG_DIR set, its .config.json wins", func(t *testing.T) {
		home, dir := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(dir, ".config.json"), `{}`)

		assert.Equal(t, filepath.Join(dir, ".config.json"), configPath(home, dir))
	})

	// The asymmetry, stated as its own case because it is the easy bug: with no
	// override the probe looks under ~/.claude, so a .config.json sitting directly
	// in $HOME must NOT be picked up.
	t.Run("no override: .config.json in $HOME itself is ignored", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".config.json"), `{}`)

		assert.Equal(t, filepath.Join(home, ".claude.json"), configPath(home, ""))
	})

	// With no home (os.UserHomeDir fails when $HOME is unset — containers, launchd)
	// and no override, joining yields the RELATIVE ".claude.json", which would be
	// read from the hook's working directory: the user's project. Reporting no path
	// at all is the only safe answer; billing then degrades to "unknown".
	t.Run("no home and no override yields no path", func(t *testing.T) {
		got := configPath("", "")
		assert.Empty(t, got, "must not fall back to a project-relative path")
	})

	// An override is absolute on its own, so it still works without a home.
	t.Run("override works with no home", func(t *testing.T) {
		dir := t.TempDir()
		assert.Equal(t, filepath.Join(dir, ".claude.json"), configPath("", dir))
	})
}

// A read with no resolvable path must fail closed rather than touching the
// working directory.
func TestReadAccountEmptyPath(t *testing.T) {
	assert.Nil(t, readAccount(""))
}

// Billing mode is decided by auth PRECEDENCE, not by the config file. Setting
// ANTHROPIC_API_KEY disables the OAuth flow, and an env-var key wins over an
// authenticated subscription — so a stale oauthAccount can sit in the config
// while traffic bills per token. Reading billingType alone reports
// "subscription" for those users, which tells a customer their real spend is not
// real spend: the worst direction to be wrong in.
func TestBillingMode(t *testing.T) {
	// A fully populated subscription account, present in every precedence case
	// below precisely because it must NOT win when auth says otherwise.
	subscribed := &account{BillingType: "stripe_subscription", SeatTier: "team_standard"}

	cases := []struct {
		name string
		auth authEnv
		acct *account
		want string
	}{
		// The counterexamples: config says subscription, auth says otherwise.
		{"bedrock beats a subscription config", authEnv{Bedrock: true}, subscribed, "bedrock"},
		{"vertex beats a subscription config", authEnv{Vertex: true}, subscribed, "vertex"},
		{"foundry beats a subscription config", authEnv{Foundry: true}, subscribed, "foundry"},
		{"api key beats a subscription config", authEnv{APIKey: true}, subscribed, "api"},

		// A bearer token means a gateway or proxy sits in front: per-token, but on
		// terms we cannot see. Reporting "subscription" here (the pre-fix
		// behaviour) told the customer their real spend was not real spend.
		{"bearer token is a gateway, not a subscription", authEnv{AuthToken: true}, subscribed, "gateway"},
		// Enterprise WIF / named profile: real Anthropic billing, contract rates.
		{"named profile is a gateway-class credential", authEnv{Profile: true}, subscribed, "gateway"},

		// A long-lived OAuth token IS subscription-backed — the docs require a
		// Pro/Max/Team/Enterprise plan for it — so it is not per-token.
		{"oauth token is subscription-backed", authEnv{OAuthToken: true}, nil, "subscription"},

		// No auth override: the config decides.
		{"subscription from the config", authEnv{}, subscribed, "subscription"},
		{"no config at all", authEnv{}, nil, "unknown"},
		{"config present but no billingType", authEnv{}, &account{SeatTier: "team_standard"}, "unknown"},

		// An API key with no config is still definitively per-token.
		{"api key with no config", authEnv{APIKey: true}, nil, "api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, billingMode(tc.auth, tc.acct))
		})
	}
}

// Unlike Codex, this harness CAN say "api": the docs make an env-var key
// definitive rather than merely consistent with per-token billing. Guard that the
// two harnesses stay distinguishable on purpose, not by accident.
func TestBillingModeEmitsAPIWhenProven(t *testing.T) {
	assert.Equal(t, "api", billingMode(authEnv{APIKey: true}, nil))
	assert.Equal(t, "unknown", billingMode(authEnv{}, nil), "absence is never api")
}

// The order is Claude Code's documented authentication precedence, not ours, so
// it is pinned rather than left to the shape of the switch. Each case sets every
// LOWER-ranked signal as well, so a reordering that let a lower tier win fails
// here. Ranks refer to the precedence list in the authentication docs.
func TestBillingModePrecedenceOrder(t *testing.T) {
	subscribed := &account{BillingType: "stripe_subscription"}

	// Each entry drops the tier above it, so every case still has all LOWER-ranked
	// signals set. A reordering that let a lower tier win fails here. Rank 4
	// (apiKeyHelper) is undetectable and so cannot appear.
	cases := []struct {
		name string
		auth authEnv
		want string
	}{
		{"rank 1 bedrock outranks all", authEnv{Bedrock: true, Vertex: true, Foundry: true, AuthToken: true, APIKey: true, OAuthToken: true, Profile: true}, "bedrock"},
		{"rank 1 vertex", authEnv{Vertex: true, Foundry: true, AuthToken: true, APIKey: true, OAuthToken: true, Profile: true}, "vertex"},
		{"rank 1 foundry", authEnv{Foundry: true, AuthToken: true, APIKey: true, OAuthToken: true, Profile: true}, "foundry"},
		{"rank 2 bearer token beats the api key", authEnv{AuthToken: true, APIKey: true, OAuthToken: true, Profile: true}, "gateway"},
		{"rank 3 api key beats the oauth token", authEnv{APIKey: true, OAuthToken: true, Profile: true}, "api"},
		{"rank 5 oauth token beats a profile", authEnv{OAuthToken: true, Profile: true}, "subscription"},
		{"rank 6 profile beats the login credential", authEnv{Profile: true}, "gateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, billingMode(tc.auth, subscribed))
		})
	}
}

// readAccount is best-effort: this is a cost annotation, never worth failing a
// span over. It returns nil only when the file cannot be read or parsed at all,
// which billingMode then reports as "unknown".
//
// Note the fields span two levels — billingType and seatTier sit under
// oauthAccount, while claudeMaxTier is top-level.
func TestReadAccount(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		assert.Nil(t, readAccount(filepath.Join(t.TempDir(), "nope.json")))
	})

	t.Run("malformed json", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "c.json")
		writeFile(t, p, `{"oauthAccount":`)
		assert.Nil(t, readAccount(p))
	})

	t.Run("valid json without oauthAccount", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "c.json")
		writeFile(t, p, `{"mcpServers":{}}`)

		acct := readAccount(p)
		require.NotNil(t, acct, "a readable file is not an unreadable one")
		assert.Empty(t, acct.BillingType, "no account means no billing type")
	})

	t.Run("billing fields across both levels", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "c.json")
		writeFile(t, p, `{
			"claudeMaxTier": "not_max",
			"oauthAccount": {"billingType": "stripe_subscription", "seatTier": "team_standard"}
		}`)

		acct := readAccount(p)
		require.NotNil(t, acct)
		assert.Equal(t, "stripe_subscription", acct.BillingType)
		assert.Equal(t, "team_standard", acct.SeatTier)
		assert.Equal(t, "not_max", acct.MaxTier, "claudeMaxTier is top-level, not nested")
	})

	// claudeMaxTier alone is still worth reporting — it feeds the plan tier even
	// when there is no oauthAccount to describe billing.
	t.Run("top-level tier without an account", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "c.json")
		writeFile(t, p, `{"claudeMaxTier": "max_20x"}`)

		acct := readAccount(p)
		require.NotNil(t, acct)
		assert.Equal(t, "max_20x", acct.MaxTier)
		assert.Empty(t, acct.BillingType)
	})

	// The identity fields are present in this fixture precisely to prove they are
	// never mapped. account names three billing fields and nothing else, so there
	// is no path by which an address or an org name could reach a span.
	t.Run("identity fields are not captured", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "c.json")
		writeFile(t, p, `{
			"oauthAccount": {
				"billingType": "stripe_subscription",
				"emailAddress": "someone@example.com",
				"displayName": "Some One",
				"accountUuid": "11111111-1111-1111-1111-111111111111",
				"organizationUuid": "22222222-2222-2222-2222-222222222222",
				"organizationName": "Example Org",
				"organizationRole": "admin"
			}
		}`)

		acct := readAccount(p)
		require.NotNil(t, acct)
		assert.Equal(t, "stripe_subscription", acct.BillingType)

		// Whatever account grows later, it must not start carrying identity.
		assert.NotContains(t, fmt.Sprintf("%+v", *acct), "example.com")
		assert.NotContains(t, fmt.Sprintf("%+v", *acct), "Example Org")
		assert.NotContains(t, fmt.Sprintf("%+v", *acct), "Some One")
	})

	// A directory where a file is expected must not panic or be treated as config.
	t.Run("path is a directory", func(t *testing.T) {
		assert.Nil(t, readAccount(t.TempDir()))
	})
}

// Plan tier has two candidate sources that overlap, and the research account
// holds BOTH claudeMaxTier "not_max" AND seatTier "team_standard" — a Team seat
// is a subscription that simply is not Max. Keying on claudeMaxTier alone would
// report "not_max", which reads as "no plan" for a paying Team customer.
//
// "not_max" is a sentinel meaning "no Max tier", not a tier in its own right.
func TestPlanType(t *testing.T) {
	cases := []struct {
		name string
		acct *account
		want string
	}{
		// The research account: Max says no, the seat says Team. Seat wins.
		{"team seat that is not max", &account{MaxTier: "not_max", SeatTier: "team_standard"}, "team_standard"},
		// A real Max tier is the more specific fact, so it takes precedence.
		{"max tier beats the seat", &account{MaxTier: "max_20x", SeatTier: "team_standard"}, "max_20x"},
		{"max tier alone", &account{MaxTier: "max_5x"}, "max_5x"},
		{"seat alone", &account{SeatTier: "team_standard"}, "team_standard"},
		// "not_max" carries no plan information by itself — omit rather than emit
		// an attribute whose value reads as an absence.
		{"not_max with no seat reports nothing", &account{MaxTier: "not_max"}, ""},
		{"empty account", &account{}, ""},
		{"no account", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, planType(tc.acct))
		})
	}
}

// read wires the env lookup and home directory together. Injected rather than
// read from the process so the whole surface — including CLAUDE_CONFIG_DIR
// rerooting — is testable without mutating the environment.
func TestRead(t *testing.T) {
	subscriptionJSON := `{"claudeMaxTier":"not_max","oauthAccount":{"billingType":"stripe_subscription","seatTier":"team_standard"}}`

	t.Run("subscription from the default location", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".claude.json"), subscriptionJSON)

		got := read(home, envOf(nil))
		assert.Equal(t, "subscription", got.BillingMode)
		assert.Equal(t, "team_standard", got.PlanType)
	})

	// The counterexample end to end: a real subscription config on disk, but
	// Bedrock in the environment. The environment must win.
	t.Run("bedrock overrides a subscription config on disk", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".claude.json"), subscriptionJSON)

		got := read(home, envOf(map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1"}))
		assert.Equal(t, "bedrock", got.BillingMode)
		// The plan is still worth reporting — it says which seat they hold even
		// though this session does not bill against it.
		assert.Equal(t, "team_standard", got.PlanType)
	})

	t.Run("CLAUDE_CONFIG_DIR is honoured", func(t *testing.T) {
		home, dir := t.TempDir(), t.TempDir()
		// Decoy in the default location: reading it would mean ignoring the override.
		writeFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"seatTier":"decoy"}}`)
		writeFile(t, filepath.Join(dir, ".claude.json"), subscriptionJSON)

		got := read(home, envOf(map[string]string{"CLAUDE_CONFIG_DIR": dir}))
		assert.Equal(t, "subscription", got.BillingMode)
		assert.Equal(t, "team_standard", got.PlanType, "must not read the decoy")
	})

	// An exported-but-empty variable is how shells leave unset values; treating it
	// as present would report "api" for someone on a subscription.
	t.Run("empty env var counts as unset", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".claude.json"), subscriptionJSON)

		got := read(home, envOf(map[string]string{"ANTHROPIC_API_KEY": ""}))
		assert.Equal(t, "subscription", got.BillingMode)
	})

	t.Run("nothing on disk and nothing in the env", func(t *testing.T) {
		got := read(t.TempDir(), envOf(nil))
		assert.Equal(t, "unknown", got.BillingMode)
		assert.Empty(t, got.PlanType)
	})
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}
