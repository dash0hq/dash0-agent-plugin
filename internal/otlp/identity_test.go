// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dash0hq/dash0-agent-plugin/internal/identity"
)

// pinIdentity replaces the host lookup for the duration of a test.
func pinIdentity(t *testing.T, info identity.Info) {
	t.Helper()
	original := resolveIdentity
	resolveIdentity = func() identity.Info { return info }
	t.Cleanup(func() { resolveIdentity = original })
}

func attrMap(attrs []Attribute) map[string]string {
	got := map[string]string{}
	for _, a := range attrs {
		if a.Value.StringValue != nil {
			got[a.Key] = *a.Value.StringValue
		}
	}
	return got
}

func TestIdentitySpanAttributes(t *testing.T) {
	t.Run("git identity is reported with its source", func(t *testing.T) {
		pinIdentity(t, identity.Info{Name: "Guy Moses", Email: "guy@dash0.com", Source: identity.SourceGit})

		got := attrMap(identitySpanAttributes(Config{}))

		assert.Equal(t, "Guy Moses", got["user.name"])
		assert.Equal(t, "guy@dash0.com", got["user.email"])
		assert.Equal(t, "git", got["dash0.gen_ai.user.identity.source"])
	})

	t.Run("OS fallback is reported and marked as a fallback", func(t *testing.T) {
		// SIG-208: this developer has no git identity. Before the fallback they
		// produced no user.name at all and showed up as a blank dashboard bucket.
		pinIdentity(t, identity.Info{Name: "guymoses", Source: identity.SourceOS})

		got := attrMap(identitySpanAttributes(Config{}))

		assert.Equal(t, "guymoses", got["user.name"])
		assert.Equal(t, "os", got["dash0.gen_ai.user.identity.source"])
		assert.NotContains(t, got, "user.email", "there is no OS source for an email address")
	})

	t.Run("no identity emits nothing at all", func(t *testing.T) {
		pinIdentity(t, identity.Info{})

		attrs := identitySpanAttributes(Config{})

		assert.Empty(t, attrs, "a blank name is worse than an absent one")
	})

	t.Run("email without a name still reports the email", func(t *testing.T) {
		pinIdentity(t, identity.Info{Email: "guy@dash0.com"})

		got := attrMap(identitySpanAttributes(Config{}))

		assert.Equal(t, "guy@dash0.com", got["user.email"])
		assert.NotContains(t, got, "dash0.gen_ai.user.identity.source",
			"a source without a name describes nothing")
	})
}

func TestIdentitySpanAttributesOmitUserInfo(t *testing.T) {
	t.Run("hashes the name and drops the email", func(t *testing.T) {
		pinIdentity(t, identity.Info{Name: "Guy Moses", Email: "guy@dash0.com", Source: identity.SourceGit})

		got := attrMap(identitySpanAttributes(Config{OmitUserInfo: true}))

		assert.Equal(t, hashIdentity("Guy Moses"), got["user.name"])
		assert.NotEqual(t, "Guy Moses", got["user.name"])
		assert.NotContains(t, got, "user.email")
	})

	t.Run("hashes a fallback name the same way", func(t *testing.T) {
		// Anonymization applies to whatever resolved, so an unconfigured
		// developer becomes a stable distinct pseudonym rather than joining
		// everyone else in one shared blank bucket.
		pinIdentity(t, identity.Info{Name: "guymoses", Source: identity.SourceOS})

		got := attrMap(identitySpanAttributes(Config{OmitUserInfo: true}))

		assert.Equal(t, hashIdentity("guymoses"), got["user.name"])
	})

	t.Run("leaves the source in the clear", func(t *testing.T) {
		// The provenance is not identifying, and hashing it would defeat the
		// admin-facing signal this attribute exists for.
		pinIdentity(t, identity.Info{Name: "guymoses", Source: identity.SourceOS})

		got := attrMap(identitySpanAttributes(Config{OmitUserInfo: true}))

		assert.Equal(t, "os", got["dash0.gen_ai.user.identity.source"])
	})

	t.Run("distinct users stay distinct after hashing", func(t *testing.T) {
		pinIdentity(t, identity.Info{Name: "alice", Source: identity.SourceOS})
		alice := attrMap(identitySpanAttributes(Config{OmitUserInfo: true}))["user.name"]
		pinIdentity(t, identity.Info{Name: "bob", Source: identity.SourceOS})
		bob := attrMap(identitySpanAttributes(Config{OmitUserInfo: true}))["user.name"]

		assert.NotEqual(t, alice, bob, "per-user attribution depends on distinct hashes")
	})
}

func TestIdentitySpanAttributesOmitIdentityFallback(t *testing.T) {
	cfg := Config{OmitIdentityFallback: true}

	t.Run("drops an OS-derived name", func(t *testing.T) {
		pinIdentity(t, identity.Info{Name: "guymoses", Source: identity.SourceOS})

		attrs := identitySpanAttributes(cfg)

		assert.Empty(t, attrs, "an approximate name must be dropped, not reported")
	})

	t.Run("keeps a real git identity", func(t *testing.T) {
		pinIdentity(t, identity.Info{Name: "Guy Moses", Email: "guy@dash0.com", Source: identity.SourceGit})

		got := attrMap(identitySpanAttributes(cfg))

		assert.Equal(t, "Guy Moses", got["user.name"])
		assert.Equal(t, "git", got["dash0.gen_ai.user.identity.source"])
	})

	t.Run("keeps the git email when only the name fell back", func(t *testing.T) {
		// user.email is git-only, so it stays trustworthy even when the name
		// came from the OS and gets dropped.
		pinIdentity(t, identity.Info{Name: "guymoses", Email: "guy@dash0.com", Source: identity.SourceOS})

		got := attrMap(identitySpanAttributes(cfg))

		assert.NotContains(t, got, "user.name")
		assert.NotContains(t, got, "dash0.gen_ai.user.identity.source")
		assert.Equal(t, "guy@dash0.com", got["user.email"])
	})

	t.Run("is off by default", func(t *testing.T) {
		pinIdentity(t, identity.Info{Name: "guymoses", Source: identity.SourceOS})

		got := attrMap(identitySpanAttributes(Config{}))

		assert.Equal(t, "guymoses", got["user.name"], "the fallback is the default behavior")
	})
}

// TestIdentityEmittedOutsideGitRepository guards the behavior that used to be
// implicit in vcs.Detect returning user fields: identity must survive when
// there is no repository at all. Cursor spawns hooks with a CWD that isn't
// always a working tree, and the user is still the user.
func TestIdentityEmittedOutsideGitRepository(t *testing.T) {
	t.Chdir(t.TempDir())
	pinIdentity(t, identity.Info{Name: "Guy Moses", Source: identity.SourceGit})

	require.Empty(t, vcsSpanAttributes(Config{}), "precondition: not inside a repository")

	got := attrMap(identitySpanAttributes(Config{}))
	assert.Equal(t, "Guy Moses", got["user.name"])
	assert.Equal(t, "git", got["dash0.gen_ai.user.identity.source"])
}

// TestSpansCarryIdentity checks the wiring: every span type built by trace.go
// must carry identity, not just the ones a reviewer happens to look at.
func TestSpansCarryIdentity(t *testing.T) {
	pinIdentity(t, identity.Info{Name: "guymoses", Source: identity.SourceOS})

	now := time.Now()
	event := map[string]any{"session_id": "s1", "tool_name": "Bash", "model": "claude-opus-5"}

	spans := map[string]Span{
		"tool":    NewToolSpan("t", "s", "p", now, now, event, false, Config{}),
		"llm":     NewLLMSpan("t", "s", "p", now, now, event, false, Config{}),
		"session": NewSessionSpan("t", "s", now, event, Config{}),
	}

	for name, span := range spans {
		got := attrMap(span.Attributes)
		assert.Equal(t, "guymoses", got["user.name"], "%s span must be attributable", name)
		assert.Equal(t, "os", got["dash0.gen_ai.user.identity.source"], "%s span", name)
	}
}
