// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package claudeconfig reads Claude Code's account configuration to determine how
// a session is billed.
//
// Cost is computed as provider list price × tokens, which is only the user's
// spend when they are billed per token. Which case applies is decided by AUTH
// PRECEDENCE, not by the config file: an env-var credential disables the OAuth
// flow, so a stale oauthAccount can sit in the config while traffic bills per
// token. See DEVELOPMENT.md for the attribute contract.
//
// Privacy: the config also holds emailAddress, displayName and organization
// identifiers. Only billing fields are decoded, so the identity fields cannot be
// mapped, logged or emitted. It holds no secrets — tokens live in the keychain.
package claudeconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Environment variables that decide auth, checked for PRESENCE only — an API
// key's value is never read.
const (
	envConfigDir = "CLAUDE_CONFIG_DIR"

	// Rank 1 — cloud providers. Billing moves to the cloud vendor entirely.
	envBedrock = "CLAUDE_CODE_USE_BEDROCK"
	envVertex  = "CLAUDE_CODE_USE_VERTEX"
	envFoundry = "CLAUDE_CODE_USE_FOUNDRY"
	// Rank 2 — bearer token, used when routing through an LLM gateway or proxy.
	envAuthToken = "ANTHROPIC_AUTH_TOKEN"
	// Rank 3 — direct API key.
	envAPIKey = "ANTHROPIC_API_KEY"
	// Rank 5 — long-lived OAuth token from `claude setup-token`; requires a
	// Pro/Max/Team/Enterprise plan, so it is subscription-backed, not per-token.
	envOAuthToken = "CLAUDE_CODE_OAUTH_TOKEN"
	// Rank 6 — Anthropic profile / Workload Identity Federation. Only the
	// env-driven forms are detected; see authFromEnv.
	envProfile         = "ANTHROPIC_PROFILE"
	envFederationRule  = "ANTHROPIC_FEDERATION_RULE_ID"
	envFederationOrgID = "ANTHROPIC_ORGANIZATION_ID"
)

// Billing modes. The string values are a wire contract shared with the Codex
// source and specified in DEVELOPMENT.md — that document, not either producer, is
// the authority. (Each harness defining its own copy risks drift; worth extracting
// to a shared package if a third producer appears.)
const (
	// BillingSubscription: flat-rate plan. Usage is rationed, not priced, so the
	// cost figure is a list-price equivalent rather than spend.
	BillingSubscription = "subscription"
	// BillingAPI: genuinely per-token at list price — the figure IS spend.
	BillingAPI = "api"
	// BillingBedrock / BillingVertex / BillingFoundry: per-token, but at a
	// negotiated cloud rate we cannot see, so the figure is neither
	// list-equivalent nor real spend. Billed by AWS / Google / Microsoft.
	BillingBedrock = "bedrock"
	BillingVertex  = "vertex"
	BillingFoundry = "foundry"
	// BillingGateway: a proxy, LLM gateway or federated enterprise credential
	// sits in front. Genuinely metered, but on terms we cannot see — same
	// reasoning as the cloud providers, different intermediary.
	BillingGateway = "gateway"
	// BillingUnknown: we looked and could not tell. Never conflate with api.
	BillingUnknown = "unknown"
)

// account is the billing subset of the config's oauthAccount. Deliberately
// minimal: the surrounding object also holds emailAddress, displayName and
// organization identifiers, and a struct that never names them cannot leak them.
type account struct {
	BillingType string // e.g. "stripe_subscription"; empty when unreported
	SeatTier    string // e.g. "team_standard"
	MaxTier     string // e.g. "not_max"
}

// authEnv records which auth-selecting variables are PRESENT. Values are never
// read — an API key's contents are irrelevant to us and must not be touched.
type authEnv struct {
	Bedrock    bool
	Vertex     bool
	Foundry    bool
	AuthToken  bool
	APIKey     bool
	OAuthToken bool
	Profile    bool
}

// billingMode applies Claude Code's documented authentication precedence — the
// order is theirs, not ours, and is load-bearing: a credential further down the
// list is not the one in use. Full table, and the two tiers a hook cannot see
// (apiKeyHelper, gateway sessions), are in DEVELOPMENT.md.
//
// The config file is consulted last, deliberately: it describes who the user is,
// not how this session bills. Consulting it first is the bug this order prevents.
func billingMode(auth authEnv, acct *account) string {
	switch {
	case auth.Bedrock:
		return BillingBedrock
	case auth.Vertex:
		return BillingVertex
	case auth.Foundry:
		return BillingFoundry
	case auth.AuthToken:
		return BillingGateway
	case auth.APIKey:
		return BillingAPI
	case auth.OAuthToken:
		return BillingSubscription
	case auth.Profile:
		return BillingGateway
	}
	if acct != nil && acct.BillingType != "" {
		return BillingSubscription
	}
	return BillingUnknown
}

// Info is what a Claude Code session reports about how it bills.
type Info struct {
	// BillingMode is always set, including BillingUnknown — recording that we
	// looked and could not tell differs from never having looked.
	BillingMode string
	// PlanType is the provider's plan identifier, empty when nothing useful is
	// known. Callers omit the attribute rather than emitting a blank.
	PlanType string
}

// Read reports how the current Claude Code session bills. Best-effort by design:
// this annotates cost and is never worth failing a span over, so every failure
// path lands on BillingUnknown.
func Read() Info {
	home, err := os.UserHomeDir()
	if err != nil {
		// Without a home directory the default config location is unknowable. An
		// explicit CLAUDE_CONFIG_DIR still works, so carry on rather than bail.
		home = ""
	}
	return read(home, os.Getenv)
}

// read takes its inputs explicitly so the whole surface, CLAUDE_CONFIG_DIR
// rerooting included, is testable without mutating the process environment.
func read(home string, getenv func(string) string) Info {
	// Presence, not value: an empty exported variable is how shells leave unset
	// values, and treating it as present would report api for a subscriber.
	present := func(key string) bool { return getenv(key) != "" }

	auth := authFromEnv(present)

	acct := readAccount(configPath(home, getenv(envConfigDir)))

	// The plan is reported even when auth overrides the mode: it still says which
	// seat the user holds, even though this session does not bill against it.
	return Info{BillingMode: billingMode(auth, acct), PlanType: planType(acct)}
}

// authFromEnv records which credential tiers are present.
//
// Only the ENV-DRIVEN profile forms are detected: a named ANTHROPIC_PROFILE, or
// the federation pair (both required). The docs also describe an "active profile"
// selected by a file in the Anthropic config directory, whose rank against the
// login credential depends on the auth mode recorded inside it — more depth than
// a cost annotation warrants, so that form is deliberately not detected and falls
// through to the config file.
func authFromEnv(present func(string) bool) authEnv {
	return authEnv{
		Bedrock:    present(envBedrock),
		Vertex:     present(envVertex),
		Foundry:    present(envFoundry),
		AuthToken:  present(envAuthToken),
		APIKey:     present(envAPIKey),
		OAuthToken: present(envOAuthToken),
		Profile:    present(envProfile) || (present(envFederationRule) && present(envFederationOrgID)),
	}
}

// maxTierNone is the sentinel claudeMaxTier carries when the account has no Max
// tier. It is an absence, not a plan, so it is never reported as one.
const maxTierNone = "not_max"

// planType reports the provider's own plan identifier, or "" when nothing useful
// is known (the caller then omits the attribute rather than emitting a blank).
//
// Two sources overlap. A real Max tier is the more specific fact and wins; the
// seat tier covers Team and Enterprise seats, which are subscriptions that are
// simply not Max — keying on claudeMaxTier alone would report "not_max" for a
// paying Team customer, which reads as "no plan".
//
// Values are the provider's vocabulary and differ per harness (Codex reports
// free/plus/pro here). Consumers should display this, never parse it.
func planType(acct *account) string {
	if acct == nil {
		return ""
	}
	if acct.MaxTier != "" && acct.MaxTier != maxTierNone {
		return acct.MaxTier
	}
	return acct.SeatTier
}

// configPath reports the file Claude Code would read. Undocumented, so taken from
// the shipped CLI bundle (2.1.81):
//
//	configDir = $CLAUDE_CONFIG_DIR ?? join(home, ".claude")
//	exists(configDir/.config.json) ? configDir/.config.json
//	                               : join($CLAUDE_CONFIG_DIR || home, ".claude.json")
//
// Mind the asymmetry: the probe is rooted at the config dir, the fallback at $HOME
// itself. Returns "" with no home and no override — joining an empty home yields a
// RELATIVE ".claude.json", which would read from the user's project directory.
// os.UserHomeDir fails whenever $HOME is unset, so that path is reachable.
func configPath(home, configDirOverride string) string {
	if home == "" && configDirOverride == "" {
		return ""
	}

	configDir := configDirOverride
	if configDir == "" {
		configDir = filepath.Join(home, ".claude")
	}
	if candidate := filepath.Join(configDir, ".config.json"); fileExists(candidate) {
		return candidate
	}

	fallbackRoot := configDirOverride
	if fallbackRoot == "" {
		fallbackRoot = home
	}
	return filepath.Join(fallbackRoot, ".claude.json")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// readAccount decodes the billing subset of the config at path.
//
// Returns nil only when the file cannot be read or parsed — which billingMode
// reports as "unknown". A readable file with no oauthAccount is NOT nil: we looked
// successfully and found no subscription, which is different from not looking.
//
// Only the fields named below are decoded. The same object carries emailAddress,
// displayName and organization identifiers; leaving them out of the struct is what
// guarantees they can never reach a span, rather than relying on care downstream.
func readAccount(path string) *account {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var doc struct {
		// claudeMaxTier is top-level, alongside oauthAccount rather than inside it.
		ClaudeMaxTier string `json:"claudeMaxTier"`
		OAuthAccount  struct {
			BillingType string `json:"billingType"`
			SeatTier    string `json:"seatTier"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}

	return &account{
		BillingType: doc.OAuthAccount.BillingType,
		SeatTier:    doc.OAuthAccount.SeatTier,
		MaxTier:     doc.ClaudeMaxTier,
	}
}
