// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package harness holds the per-coding-agent constants and environment lookups
// shared by the cmd/*-on-event entrypoints. Each entrypoint declares one
// Harness value; everything that differs between agents lives in its fields, so
// the lookup logic exists once.
package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dash0hq/dash0-agent-plugin/internal/config"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// The agents this plugin supports. Entrypoints reference these values rather
// than declaring their own, so the tests in this package assert the constants that
// actually ship.
var (
	// Claude DataSubdir is set for correctness but unused. Claude Code supplies the data
	// directory as CLAUDE_PLUGIN_DATA, which run() requires rather than falling back.
	Claude = Harness{Name: "claude-code", EnvPrefix: "CLAUDE", DataSubdir: "claude", ConfigDir: ".claude", Provider: "anthropic"}
	// Cursor Provider is intentionally empty.It proxies many vendors, so
	// the provider is derived per-event from the model name
	Cursor = Harness{Name: "cursor", EnvPrefix: "CURSOR", DataSubdir: "cursor", ConfigDir: ".cursor"}
	// Codex Provider is set to openai (Codex is single-vendor). The GenAI layer still
	// resolves provider per-event from the model name (e.g. gpt-*/o*/codex-* → openai)
	// and only falls back to this value when a model is absent.
	Codex = Harness{Name: "codex", EnvPrefix: "CODEX", DataSubdir: "codex", ConfigDir: ".codex", Provider: "openai"}
	// Copilot Provider is intentionally empty. It serves several vendors,
	// so the provider is resolved per-event from the model name rather than
	// forced to one value.
	Copilot = Harness{Name: "github-copilot-cli", EnvPrefix: "COPILOT", DataSubdir: "copilot", ConfigDir: ".copilot"}
)

// Harness names one coding agent's environment conventions.
type Harness struct {
	// Name is the agent platform's identity, with two uses. It is reported
	// verbatim as gen_ai.harness.name (see HarnessName), which is never
	// user-configurable because it says which tool produced the telemetry. It is
	// also the default agent name (see AgentName) when the user configures none,
	// by convention rather than necessity — the two diverge as soon as AGENT_NAME
	// is set, which is the point of having both attributes.
	Name string
	// EnvPrefix is the agent's environment-variable prefix, without the
	// trailing underscore.
	EnvPrefix string
	// DataSubdir is this agent's directory under the shared state root.
	DataSubdir string
	// ConfigDir is the agent's own dot-directory, which may hold a
	// config.Name file. It is looked for twice: relative to the project the
	// agent runs in, then inside the user's home directory.
	ConfigDir string
	// Provider is the fallback gen_ai.provider.name for events whose model
	// cannot be inferred (SessionStart, PreToolUse, ...). Leave it empty for an
	// agent that proxies several vendors.
	Provider string
}

// Config builds the exporter configuration for this agent. Every entrypoint
// resolved the same fields the same way, so the shape lives here once.
//
// Config validates the endpoint before it returns, so a malformed URL is logged
// to stderr and cleared here, so export is disabled.
func (h Harness) Config() otlp.Config {
	cfg := otlp.Config{
		OTLPUrl:              h.PluginOption("OTLP_URL"),
		AuthToken:            h.authToken(),
		Dataset:              h.PluginOption("DATASET"),
		AgentName:            h.AgentName(),
		HarnessName:          h.HarnessName(),
		Provider:             h.Provider,
		TeamName:             h.PluginOption("TEAM_NAME"),
		OmitUserInfo:         h.PluginOptionBoolDefault("OMIT_USER_INFO", false),
		OmitIdentityFallback: h.PluginOptionBoolDefault("OMIT_IDENTITY_FALLBACK", false),
		OmitIO:               h.PluginOptionBoolDefault("OMIT_IO", true),
		Debug:                h.PluginOptionBool("DEBUG"),
		DebugFile:            h.PluginOption("DEBUG_FILE"),
	}
	cfg.ValidateURL()
	return cfg
}

// DataDir returns the root for this agent's per-session scratch state.
//
// Precedence, highest first:
//
//  1. <EnvPrefix>_PLUGIN_DATA — the agent's own override
//  2. DASH0_PLUGIN_DATA — cross-agent override
//  3. $XDG_STATE_HOME/dash0-agent-plugin/<DataSubdir>
//  4. ~/.local/state/dash0-agent-plugin/<DataSubdir>
//
// The bootstrap scripts derive their binary cache path the same way, except Codex:
// codex-on-event.sh caches under bare PLUGIN_DATA but never exports it, so for a
// marketplace install the cache and the session state sit in different roots.
// Reading bare PLUGIN_DATA here would align them and relocate existing state, so
// it is a migration rather than a cleanup. FEATURE_MATRIX.md records the split.
func (h Harness) DataDir() (string, error) {
	if v := os.Getenv(h.EnvPrefix + "_PLUGIN_DATA"); v != "" {
		return v, nil
	}
	if v := os.Getenv("DASH0_PLUGIN_DATA"); v != "" {
		return v, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving HOME: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "dash0-agent-plugin", h.DataSubdir), nil
}

// HarnessName returns the platform identity for gen_ai.harness.name. Unlike
// AgentName there is deliberately no override: the attribute records which
// coding agent emitted the span, so a user renaming their service must not be
// able to disguise it.
func (h Harness) HarnessName() string {
	return h.Name
}

// AgentName returns the configured agent name, falling back to h.Name.
func (h Harness) AgentName() string {
	if v := h.PluginOption("AGENT_NAME"); v != "" {
		return v
	}
	return h.Name
}

// PluginOptionSecure reads <EnvPrefix>_PLUGIN_OPTION_<key>. Secrets are passed
// only through this prefixed form, never through a DASH0_* variable, so they do
// not leak into environments the agent hands to tool subprocesses.
func (h Harness) PluginOptionSecure(key string) string {
	return os.Getenv(h.EnvPrefix + "_PLUGIN_OPTION_" + key)
}

// PluginOption reads <EnvPrefix>_PLUGIN_OPTION_<key>, falling back to
// DASH0_<key> and then to the lower-cased key in the configuration file. Use it
// for ordinary configuration; secrets must use PluginOptionSecure, which has no
// DASH0_* fallback so a token cannot be picked up from an environment the agent
// hands to tool subprocesses.
func (h Harness) PluginOption(key string) string {
	if v := h.PluginOptionSecure(key); v != "" {
		return v
	}
	if v := h.Dash0Env(key); v != "" {
		return v
	}
	return h.configFile().Get(strings.ToLower(key))
}

// configFile reads this agent's configuration file, preferring the copy in the
// project the agent is running in over the one in the user's home directory.
//
// Entrypoints chdir into the event's cwd before reading configuration (see
// ChdirToEventCwd), so the relative path resolves against the user's project
// rather than wherever the agent spawned the binary.
func (h Harness) configFile() *config.File {
	paths := []string{filepath.Join(h.ConfigDir, config.Name)}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, h.ConfigDir, config.Name))
	}
	return config.Load(paths...)
}

// Enabled reports whether the configuration file permits this agent to emit
// telemetry. Only `enabled: false` disables it; an absent file or key means yes.
func (h Harness) Enabled() bool {
	return h.configFile().Enabled()
}

// authToken resolves the Dash0 token. Highest precedence first:
//
//  1. the macOS keychain, when a keychain service is configured and the lookup
//     succeeds;
//  2. <EnvPrefix>_PLUGIN_OPTION_AUTH_TOKEN;
//  3. auth_token in the configuration file.
//
// Steps 2 and 3 are PluginOption's order — environment before file — minus the
// DASH0_* fallback, which a secret must never have: that variable reaches the
// environments the agent hands to tool subprocesses.
//
// The keychain outranks both on purpose, and it is the one documented ordering
// here: plugin.json and the Claude README both promise that a successful lookup
// takes precedence over AUTH_TOKEN. It is what makes a managed rollout able to
// ship a pointer, safe to put in managed-settings.json, instead of the secret.
func (h Harness) authToken() string {
	if service := h.PluginOption("AUTH_TOKEN_KEYCHAIN_SERVICE"); service != "" {
		if token := keychainToken(service, h.PluginOption("AUTH_TOKEN_KEYCHAIN_ACCOUNT")); token != "" {
			return token
		}
	}
	if token := h.PluginOptionSecure("AUTH_TOKEN"); token != "" {
		return token
	}
	return h.configFile().Get("auth_token")
}

// PluginOptionBool is PluginOption as a boolean, false when unset.
func (h Harness) PluginOptionBool(key string) bool {
	return h.PluginOptionBoolDefault(key, false)
}

// PluginOptionBoolDefault is PluginOption as a boolean, returning defaultVal when
// unset or blank. "true" and "1" are true; anything else present is false.
func (h Harness) PluginOptionBoolDefault(key string, defaultVal bool) bool {
	v := strings.ToLower(strings.TrimSpace(h.PluginOption(key)))
	if v == "" {
		return defaultVal
	}
	return v == "true" || v == "1"
}

// Dash0Env reads DASH0_<key>.
func (h Harness) Dash0Env(key string) string {
	return os.Getenv("DASH0_" + key)
}

// ChdirToEventCwd switches to the working directory named in a hook payload, so
// repository detection and relative config lookups resolve against the user's
// project rather than wherever the agent happened to spawn the binary. A missing
// or unusable cwd is ignored: the chdir is an improvement, not a requirement.
func (h Harness) ChdirToEventCwd(event map[string]any) {
	cwd, ok := event["cwd"].(string)
	if !ok || cwd == "" {
		return
	}
	_ = os.Chdir(cwd)
}
