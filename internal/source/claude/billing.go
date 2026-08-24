// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package claude reads Claude Code's local state. Today that is account
// configuration, to determine how a session is billed.
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
package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Environment variables that decide auth. The credentials are checked for
// PRESENCE only — an API key's value is never read. The rank-1 flags are the
// exception: they carry a boolean, so their value decides whether they count at
// all (see truthy).
const (
	envConfigDir = "CLAUDE_CONFIG_DIR"

	// Rank 1 — cloud providers. Billing moves to the cloud vendor entirely.
	// Listed in the CLI's own resolution order; see authTiers.
	envBedrock              = "CLAUDE_CODE_USE_BEDROCK"
	envFoundry              = "CLAUDE_CODE_USE_FOUNDRY"
	envAnthropicAWS         = "CLAUDE_CODE_USE_ANTHROPIC_AWS"
	envAnthropicGoogleCloud = "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD"
	envMantle               = "CLAUDE_CODE_USE_MANTLE"
	envVertex               = "CLAUDE_CODE_USE_VERTEX"
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
	// BillingMeteredExternal: per-token, but somebody else meters it at a rate we
	// cannot see, so the figure is neither a list-price equivalent nor real spend.
	// Which intermediary travels separately, as the provider.
	BillingMeteredExternal = "metered_external"
	// BillingUnknown: we looked and could not tell. Never conflate with api.
	BillingUnknown = "unknown"
)

// Billing providers — who meters a BillingMeteredExternal session. A separate
// dimension from the mode on purpose: "is this per-token" and "who bills it" are
// different questions, and keeping them apart means a consumer can read the mode
// alone without being misled, a backend can ask for all metered sessions without
// enumerating vendors, and a new provider is a value here rather than a new mode.
//
// ProviderGateway covers an unnamed intermediary — an LLM gateway, proxy, or
// federated enterprise credential — where we know somebody else sets the rate but
// not who.
const (
	ProviderBedrock = "bedrock"
	ProviderVertex  = "vertex"
	ProviderFoundry = "foundry"
	ProviderGateway = "gateway"
)

// account is the billing subset of the config's oauthAccount. Deliberately
// minimal: the surrounding object also holds emailAddress, displayName and
// organization identifiers, and a struct that never names them cannot leak them.
type account struct {
	BillingType string // e.g. "stripe_subscription"; empty when unreported
	SeatTier    string // e.g. "team_standard"
	MaxTier     string // e.g. "not_max"
}

// authTier is one rung of Claude Code's authentication precedence: the condition
// that selects it, and the billing mode it implies. An empty mode means "this tier
// is plan-backed — resolve from the account".
//
// Keeping the ranks as ordered DATA rather than a switch means the order is the
// table, and adding a provider is one line.
type authTier struct {
	name string
	mode string
	// provider names the intermediary when mode is BillingMeteredExternal.
	provider string
	// set reports whether this tier selects the session's provider. Built by
	// flag or presence, which read the environment two different ways.
	set func(getenv func(string) string) bool
}

// authTiers is the documented precedence, highest first. Rank 4 (apiKeyHelper) is
// absent because a hook cannot observe it; see DEVELOPMENT.md.
var authTiers = []authTier{
	// Rank 1, in the CLI's own order (2.1.238 provider resolver). All six are
	// boolean FLAGS rather than credentials: see flag. The provider is who meters
	// the session, so the two AWS-family selectors share ProviderBedrock and the
	// Google one shares ProviderVertex — a more precise vendor name would expand
	// the attribute's value set without changing who bills.
	{name: "bedrock", mode: BillingMeteredExternal, provider: ProviderBedrock, set: flag(envBedrock)},
	{name: "foundry", mode: BillingMeteredExternal, provider: ProviderFoundry, set: flag(envFoundry)},
	{name: "anthropic on aws", mode: BillingMeteredExternal, provider: ProviderBedrock, set: flag(envAnthropicAWS)},
	{name: "anthropic on google cloud", mode: BillingMeteredExternal, provider: ProviderVertex, set: flag(envAnthropicGoogleCloud)},
	{name: "mantle", mode: BillingMeteredExternal, provider: ProviderBedrock, set: flag(envMantle)},
	{name: "vertex", mode: BillingMeteredExternal, provider: ProviderVertex, set: flag(envVertex)},
	{name: "bearer token", mode: BillingMeteredExternal, provider: ProviderGateway, set: presence(envAuthToken)},
	{name: "api key", mode: BillingAPI, set: presence(envAPIKey)},
	// Plan-backed: mode comes from the account, not from the tier.
	{name: "setup token", mode: "", set: presence(envOAuthToken)},
	{name: "profile/federation", mode: BillingMeteredExternal, provider: ProviderGateway, set: func(getenv func(string) string) bool {
		// Only the env-driven forms: a named profile, or the federation pair. The
		// docs also describe an "active profile" chosen by a file in the Anthropic
		// config directory, whose rank against /login depends on an auth mode
		// recorded inside it — more depth than a cost annotation warrants, so it
		// falls through to the account.
		return getenv(envProfile) != "" ||
			(getenv(envFederationRule) != "" && getenv(envFederationOrgID) != "")
	}},
}

// presence selects a tier on a credential existing. Its value is a secret we
// never read, so any non-empty string counts; an empty one is how a shell leaves
// an exported-but-unset variable.
func presence(key string) func(func(string) string) bool {
	return func(getenv func(string) string) bool { return getenv(key) != "" }
}

// flag selects a tier on a boolean provider flag, parsed exactly as Claude Code
// parses it: the shipped CLI (2.1.238) declares every CLAUDE_CODE_USE_* selector
// as `Ge.bool()`, coerced with
// `["1","true","yes","on"].includes(String(v).toLowerCase().trim())`.
//
// Matching that set exactly matters in both directions. Counting any non-empty
// value reports metered_external for CLAUDE_CODE_USE_BEDROCK=0, telling a
// subscriber their figure is metered by AWS; counting only "1" and "true" reports
// subscription for =yes, telling a real Bedrock session its list-price figure is
// its allowance.
func flag(key string) func(func(string) string) bool {
	return func(getenv func(string) string) bool {
		switch strings.ToLower(strings.TrimSpace(getenv(key))) {
		case "1", "true", "yes", "on":
			return true
		}
		return false
	}
}

// billing applies Claude Code's documented authentication precedence — the
// order is theirs, not ours, and is load-bearing: a credential further down the
// list is not the one in use. Full table, and the two tiers a hook cannot see
// (apiKeyHelper, gateway sessions), are in DEVELOPMENT.md.
//
// The config file is consulted last, deliberately: it describes who the user is,
// not how this session bills. Consulting it first is the bug this order prevents.
func billing(getenv func(string) string, acct *account) (mode, provider string) {
	for _, tier := range authTiers {
		if !tier.set(getenv) {
			continue
		}
		if tier.mode != "" {
			return tier.mode, tier.provider
		}
		// A plan-backed credential (rank 5) is not proof of a subscription — an
		// enterprise org can sit on usage-based billing — so it resolves exactly
		// as the /login credential at rank 7 does. No intermediary either way.
		return modeFromAccount(acct), ""
	}
	return modeFromAccount(acct), ""
}

// modeFromAccount resolves how a plan-backed account bills. Unknown when we
// cannot tell: a credential we recognise is not a licence to assume its billing.
func modeFromAccount(acct *account) string {
	if acct == nil {
		return BillingUnknown
	}
	// An enterprise seat can carry usage-based billing directly, with no
	// billingType reported alongside it.
	if acct.SeatTier == seatTierEnterpriseUsageBased {
		return BillingAPI
	}
	switch acct.BillingType {
	case billingTypeUsageBased:
		return BillingAPI
	case billingTypeStripe, billingTypeStripeContracted,
		billingTypeApple, billingTypeGooglePlay:
		return BillingSubscription
	}
	return BillingUnknown
}

// billingType values carried by oauthAccount. Not all of them mean "subscription":
// a Claude Console account — the path for organizations that prefer API-based
// billing — logs in like any other and carries an oauthAccount, but bills per
// token. Claude Code itself branches on usage_based for the same reason.
//
// An unrecognised value falls through to BillingUnknown rather than being assumed
// a subscription: a future billing type we have never seen is not a licence to
// claim the cost figure is not the user's spend.
const (
	// seatTierEnterpriseUsageBased is a seatTier, not a billingType — Claude Code
	// gates on `seatTier === "enterprise_usage_based"` to identify an enterprise
	// org billing per token.
	seatTierEnterpriseUsageBased = "enterprise_usage_based"

	billingTypeUsageBased       = "usage_based"
	billingTypeStripe           = "stripe_subscription"
	billingTypeStripeContracted = "stripe_subscription_contracted"
	billingTypeApple            = "apple_subscription"
	billingTypeGooglePlay       = "google_play_subscription"
)

// Billing is what a Claude Code session reports about how it bills.
type Billing struct {
	// BillingMode is always set, including BillingUnknown — recording that we
	// looked and could not tell differs from never having looked.
	BillingMode string
	// Provider names who meters the session, and is set only when BillingMode is
	// BillingMeteredExternal. Orthogonal to the mode: a consumer can read either
	// alone without being misled.
	Provider string
	// PlanType is the provider's plan identifier, empty when nothing useful is
	// known. Callers omit the attribute rather than emitting a blank.
	PlanType string
}

// ReadBilling reports how the current Claude Code session bills. Best-effort by design:
// this annotates cost and is never worth failing a span over, so every failure
// path lands on BillingUnknown.
func ReadBilling() Billing {
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
func read(home string, getenv func(string) string) Billing {
	acct := readAccount(configPath(home, getenv(envConfigDir)))

	// The plan is reported even when auth overrides the mode: it still says which
	// seat the user holds, even though this session does not bill against it.
	mode, provider := billing(getenv, acct)
	return Billing{BillingMode: mode, Provider: provider, PlanType: planType(acct)}
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
// Returns nil only when the file cannot be read or parsed — which modeFromAccount
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
