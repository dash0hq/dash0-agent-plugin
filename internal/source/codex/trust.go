// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Markers delimit the block install-codex.sh appends to the user's config.toml,
// so a re-install can replace it and uninstall can strip it without touching any
// user-authored hooks.
const (
	ManagedBlockBegin = "# >>> dash0-agent-plugin (managed) — do not edit >>>"
	ManagedBlockEnd   = "# <<< dash0-agent-plugin (managed) <<<"
)

// Codex enforces hook trust (since v0.129): a hook only runs without a `/hooks`
// prompt if config.toml carries a matching trusted_hash under
// [hooks.state."<config-path>:<event_snake>:<group_index>:<handler_index>"].
// There is no supported API to request trust (openai/codex#21615), so the
// installer reproduces Codex's own hash so a fresh install runs non-interactively.
//
// The hash is Codex's "config fingerprint" of the normalized hook identity:
//
//	sha256( canonical-JSON({event_name, matcher, hooks:[handler]}) )
//
// where canonical JSON has recursively sorted object keys and no whitespace, and
// the handler is normalized to {type:"command", command, statusMessage?, async,
// timeout}. Codex fills timeout with its default (600s) and async with false when
// the block omits them, and drops None fields (TOML has no null) — so we hash
// with those exact normalized values. Verified byte-for-byte against real
// Codex 0.142.5 trusted_hash entries (see trust_test.go).
//
// This reproduces private, version-fragile Codex internals; the no-bypass e2e
// canary runs real Codex against these hashes so a serialization change fails CI
// instead of silently disabling telemetry.
const (
	// codexHookTimeoutDefault is the timeout_sec Codex normalizes an omitted
	// timeout to; it participates in the trust hash.
	codexHookTimeoutDefault = 600
	// codexHookMatcher is the matcher we register every hook under.
	codexHookMatcher = "*"
	// codexHookStatusMessage shows in Codex's hook log; it participates in the
	// trust hash, so the installed block MUST carry this exact value.
	codexHookStatusMessage = "dash0"
)

// HookEvent pairs a Codex hook event's config name (PascalCase, used in the
// [[hooks.<Name>]] table) with its state-key label (snake_case, used in the
// [hooks.state] key and the trust-hash identity).
type HookEvent struct {
	ConfigName string // e.g. "PreToolUse"
	KeyLabel   string // e.g. "pre_tool_use"
	// HasMatcher is whether Codex keeps the matcher in this event's normalized
	// hook identity. Events with nothing to match against (Stop, UserPromptSubmit)
	// have their matcher normalized to None, which changes the trust hash — so the
	// hash for those MUST omit the matcher or Codex silently treats the hook as
	// untrusted. Verified against real 0.142.5 oracles for all ten events.
	HasMatcher bool
}

// HookEvents is the full set of Codex hook events the plugin registers, in a
// stable order. Codex 0.142.5 exposes exactly these ten.
var HookEvents = []HookEvent{
	{"SessionStart", "session_start", true},
	{"UserPromptSubmit", "user_prompt_submit", false},
	{"PreToolUse", "pre_tool_use", true},
	{"PostToolUse", "post_tool_use", true},
	{"Stop", "stop", false},
	{"SubagentStart", "subagent_start", true},
	{"SubagentStop", "subagent_stop", true},
	{"PreCompact", "pre_compact", true},
	{"PostCompact", "post_compact", true},
	{"PermissionRequest", "permission_request", true},
}

// TrustHash returns the Codex trusted_hash ("sha256:<hex>") for a command hook
// registered on the given event. hasMatcher must reflect whether Codex keeps the
// matcher for this event (see HookEvent.HasMatcher); command and statusMessage
// must match what the installer writes, or Codex treats the hook as modified and
// skips it.
func TrustHash(eventKeyLabel string, hasMatcher bool, command, statusMessage string) (string, error) {
	handler := map[string]any{
		"type":    "command",
		"command": command,
		"async":   false,
		"timeout": codexHookTimeoutDefault,
	}
	// A None statusMessage is dropped from the identity (TOML has no null), so
	// only include it when non-empty — matching how the block is written.
	if statusMessage != "" {
		handler["statusMessage"] = statusMessage
	}
	identity := map[string]any{
		"event_name": eventKeyLabel,
		"hooks":      []any{handler},
	}
	// Only matcher-bearing events keep the matcher in the hashed identity.
	if hasMatcher {
		identity["matcher"] = codexHookMatcher
	}

	// Match serde_json exactly: sorted keys (Go default for maps), no whitespace,
	// and no HTML escaping of <, >, & (serde does not escape them).
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(identity); err != nil {
		return "", fmt.Errorf("encoding hook identity: %w", err)
	}
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// RenderManagedBlock returns the marker-delimited TOML the installer appends to
// config.toml: a [[hooks.<Event>]] command block plus a matching [hooks.state]
// trusted_hash for every event, so a fresh install runs without a /hooks prompt.
//
// configPath is the absolute path of the config file the block is written into
// (it is part of each trust-state key). command is the exact hook command
// string; it must be written verbatim into the block (single-quoted TOML) and is
// what gets hashed. existingConfig is the current config.toml content MINUS any
// prior managed block, used to count pre-existing [[hooks.<Event>]] groups so our
// appended group's index (in the state key) is correct even when the user has
// their own Codex hooks.
func RenderManagedBlock(configPath, command, existingConfig string) (string, error) {
	if strings.Contains(command, "'") {
		return "", fmt.Errorf("hook command contains a single quote, which cannot be written as a TOML literal string: %q", command)
	}

	var b strings.Builder
	b.WriteString(ManagedBlockBegin + "\n")
	b.WriteString("# Managed by dash0-agent-plugin. Re-run install-codex.sh to update; run uninstall-codex.sh to remove.\n")

	var state strings.Builder
	for _, e := range HookEvents {
		groupIndex := countHookGroups(existingConfig, e.ConfigName)

		fmt.Fprintf(&b, "\n[[hooks.%s]]\nmatcher = \"%s\"\n[[hooks.%s.hooks]]\ntype = \"command\"\ncommand = '%s'\nstatusMessage = \"%s\"\n",
			e.ConfigName, codexHookMatcher, e.ConfigName, command, codexHookStatusMessage)

		// The block writes matcher = "*" uniformly; Codex normalizes it away for
		// matcher-less events when hashing, so the hash must follow e.HasMatcher.
		hash, err := TrustHash(e.KeyLabel, e.HasMatcher, command, codexHookStatusMessage)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&state, "\n[hooks.state.\"%s:%s:%d:0\"]\ntrusted_hash = \"%s\"\n",
			configPath, e.KeyLabel, groupIndex, hash)
	}

	// Group all [hooks.state] tables after the [[hooks]] blocks so the emitted
	// TOML is easy to read and the state keys sit together.
	b.WriteString(state.String())
	b.WriteString("\n" + ManagedBlockEnd + "\n")
	return b.String(), nil
}

// StripManagedBlock removes the marker-delimited managed block (inclusive) from
// config content, returning the rest. Used before counting groups on re-install
// and to keep uninstall/emit logic in one tested place. If markers are absent the
// content is returned unchanged.
func StripManagedBlock(config string) string {
	begin := strings.Index(config, ManagedBlockBegin)
	if begin == -1 {
		return config
	}
	end := strings.Index(config, ManagedBlockEnd)
	if end == -1 || end < begin {
		// Malformed (begin without end) — drop from the marker onward.
		return strings.TrimRight(config[:begin], "\n")
	}
	end += len(ManagedBlockEnd)
	// Consume the trailing newline after the end marker if present.
	if end < len(config) && config[end] == '\n' {
		end++
	}
	stripped := strings.TrimRight(config[:begin], "\n")
	rest := config[end:]
	if stripped != "" && rest != "" {
		return stripped + "\n" + rest
	}
	return stripped + rest
}

// countHookGroups counts array-of-table headers [[hooks.<Event>]] in config for
// the given PascalCase event name — the number of pre-existing matcher groups,
// which is the group index our appended group takes.
func countHookGroups(config, configName string) int {
	re := regexp.MustCompile(`(?m)^[ \t]*\[\[hooks\.` + regexp.QuoteMeta(configName) + `\]\][ \t]*$`)
	return len(re.FindAllString(config, -1))
}
