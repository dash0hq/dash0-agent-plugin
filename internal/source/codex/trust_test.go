package codex

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTrustHashMatchesRealCodexOracles pins the reproduction against real
// trusted_hash values written by Codex 0.142.5 into a config.toml. The hook
// command, matcher ("*"), and statusMessage ("dash0 capture") below are the
// exact values from that config; only the event label varies. If Codex changes
// its hook-identity serialization, these vectors break — that's the signal to
// re-verify (the no-bypass e2e canary catches it against a live binary too).
func TestTrustHashMatchesRealCodexOracles(t *testing.T) {
	const (
		command = `python3 "/Users/guymoses/.codex/hooks/dash0_capture.py"`
		status  = "dash0 capture"
	)
	// Golden: event key label -> {hasMatcher, Codex-written trusted_hash}. All ten
	// events verified. Stop and UserPromptSubmit hash WITHOUT the matcher (Codex
	// normalizes it to None for events with nothing to match); the rest keep it.
	type oracle struct {
		hasMatcher bool
		hash       string
	}
	oracles := map[string]oracle{
		"session_start":      {true, "sha256:e8921449ec9766690a30cbc6a68bf459ce0be93a061acbeb20b0a5beb5fc579a"},
		"user_prompt_submit": {false, "sha256:f1c538f65d6b831e8d357406f3d31f5a3812735d96927d72fd4e8e8711a3c368"},
		"pre_tool_use":       {true, "sha256:2b1107769357c304823db5e1677922cbaf815706fd2226efffd3a6ed5c531dc2"},
		"post_tool_use":      {true, "sha256:76a4262fc3b174f56d4180b92e55cc613dd5b941415e0f98f8198d041572fd09"},
		"stop":               {false, "sha256:9b44a430246550d6bf6ae8b250e66c5926238b97b50b77da7c74522ade8c5df8"},
		"permission_request": {true, "sha256:54e0238a6a8699f8493d9ae6012e5707b600b981db66f98fae2f7cdda92293b3"},
		"pre_compact":        {true, "sha256:51772c948e900ca85ca1c25ee97d45167deca12961d664bc7d30e1ab56a62378"},
		"post_compact":       {true, "sha256:0f3f6e9ba8ebd3e66532a3fc04ad6c1285bbb6ddf9cc0b272bf995b976cfef6b"},
		"subagent_start":     {true, "sha256:d0ef9d51acbaab1b98d712c10270528041320d68bdb7457fbeee48e1c55a8703"},
		"subagent_stop":      {true, "sha256:03979c8047705e864045a3a80fd35d596681ace80cca494b2338d3cf610dd590"},
	}
	// Every registered event must have an oracle here, so a new event can't ship
	// untested.
	assert.Len(t, oracles, len(HookEvents))
	for label, o := range oracles {
		t.Run(label, func(t *testing.T) {
			got, err := TrustHash(label, o.hasMatcher, command, status)
			require.NoError(t, err)
			assert.Equal(t, o.hash, got)
		})
	}
}

// TestHookEventsMatcherPartition pins which events drop the matcher from the
// hashed identity — the distinction that determines trust for Stop/UserPromptSubmit.
func TestHookEventsMatcherPartition(t *testing.T) {
	want := map[string]bool{
		"session_start": true, "user_prompt_submit": false, "pre_tool_use": true,
		"post_tool_use": true, "stop": false, "subagent_start": true,
		"subagent_stop": true, "pre_compact": true, "post_compact": true,
		"permission_request": true,
	}
	for _, e := range HookEvents {
		assert.Equal(t, want[e.KeyLabel], e.HasMatcher, "HasMatcher for %s", e.KeyLabel)
	}
}

// TestTrustHashDropsEmptyStatusMessage documents that an empty statusMessage is
// omitted from the identity (TOML has no null), producing a different hash than
// a present one — so the installer must keep the written block and the hash in
// sync on this field.
func TestTrustHashDropsEmptyStatusMessage(t *testing.T) {
	withStatus, err := TrustHash("stop", false, "echo hi", "dash0")
	require.NoError(t, err)
	without, err := TrustHash("stop", false, "echo hi", "")
	require.NoError(t, err)
	assert.NotEqual(t, withStatus, without)
}

func TestRenderManagedBlockFreshConfig(t *testing.T) {
	cmd := `bash "/home/u/.local/state/dash0-agent-plugin/codex/codex-on-event.sh"`
	cfg := "/home/u/.codex/config.toml"
	out, err := RenderManagedBlock(cfg, cmd, "")
	require.NoError(t, err)

	assert.Contains(t, out, ManagedBlockBegin)
	assert.Contains(t, out, ManagedBlockEnd)
	// A block + trust key for every event; fresh config → group index 0.
	for _, e := range HookEvents {
		assert.Contains(t, out, "[[hooks."+e.ConfigName+"]]")
		assert.Contains(t, out, fmt.Sprintf("[hooks.state.%q]", cfg+":"+e.KeyLabel+":0:0"))
	}
	// The emitted hash matches TrustHash for the same command.
	want, err := TrustHash("session_start", true, cmd, codexHookStatusMessage)
	require.NoError(t, err)
	assert.Contains(t, out, want)
	// The written block carries the statusMessage the hash depends on.
	assert.Contains(t, out, `statusMessage = "`+codexHookStatusMessage+`"`)
}

// A pre-existing user hook group shifts our appended group's trust-key index.
func TestRenderManagedBlockIndexesAfterExistingGroups(t *testing.T) {
	cfg := "/home/u/.codex/config.toml"
	existing := "[[hooks.PreToolUse]]\nmatcher = \"*\"\n[[hooks.PreToolUse.hooks]]\ntype = \"command\"\ncommand = 'echo user'\n"
	out, err := RenderManagedBlock(cfg, `bash "x"`, existing)
	require.NoError(t, err)
	// Our PreToolUse group is the SECOND group for that event → index 1.
	assert.Contains(t, out, fmt.Sprintf("[hooks.state.%q]", cfg+":pre_tool_use:1:0"))
	// An event the user didn't touch stays at index 0.
	assert.Contains(t, out, fmt.Sprintf("[hooks.state.%q]", cfg+":stop:0:0"))
}

func TestRenderManagedBlockRejectsSingleQuote(t *testing.T) {
	_, err := RenderManagedBlock("/c.toml", `bash "/home/o'brien/x.sh"`, "")
	assert.Error(t, err)
}

func TestStripManagedBlock(t *testing.T) {
	cfg := "/c.toml"
	user := "model = \"gpt-5.5\"\n\n[[hooks.PreToolUse]]\nmatcher = \"*\"\n"
	block, err := RenderManagedBlock(cfg, `bash "x"`, user)
	require.NoError(t, err)

	combined := user + "\n" + block
	stripped := StripManagedBlock(combined)
	assert.NotContains(t, stripped, ManagedBlockBegin)
	assert.NotContains(t, stripped, "hooks.state")
	assert.Contains(t, stripped, `model = "gpt-5.5"`)
	assert.Contains(t, stripped, "[[hooks.PreToolUse]]") // user's own hook preserved
	// Idempotent: stripping again is a no-op.
	assert.Equal(t, stripped, StripManagedBlock(stripped))
}

// TestHookEventsCoversTen guards the event set Codex 0.142.5 exposes.
func TestHookEventsCoversTen(t *testing.T) {
	assert.Len(t, HookEvents, 10)
	seen := map[string]bool{}
	for _, e := range HookEvents {
		assert.NotEmpty(t, e.ConfigName)
		assert.NotEmpty(t, e.KeyLabel)
		assert.False(t, seen[e.KeyLabel], "duplicate event %s", e.KeyLabel)
		seen[e.KeyLabel] = true
	}
}
