// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/dash0hq/dash0-agent-plugin/internal/source/copilot"
)

// Both models reach the event, under keys the OTLP layer maps to their own
// semconv names. Only the responding one can be priced: Copilot reports "auto"
// as the requested model whenever the user has not pinned one, and that matches
// no row in the collector's pricing table, so a turn carrying only "auto"
// reaches ClickHouse with real token counts and no cost at all (SIG-528).
func TestAttachUsage_carriesBothModels(t *testing.T) {
	event := map[string]any{}
	attachUsage(event, &copilot.Usage{
		InputTokens: 24085, OutputTokens: 47,
		Model:         "auto",
		ResponseModel: "gpt-5.6-luna",
	})

	assert.Equal(t, "auto", event["model"], "what was asked for must survive")
	assert.Equal(t, "gpt-5.6-luna", event["response_model"])
}

// A model already on the event came from the hook payload, which is closer to
// the turn than the file rollup is, so neither key may overwrite it.
func TestAttachUsage_doesNotOverwriteEitherModel(t *testing.T) {
	event := map[string]any{"model": "pinned", "response_model": "pinned-response"}
	attachUsage(event, &copilot.Usage{
		InputTokens: 1,
		Model:       "auto", ResponseModel: "gpt-5.6-luna",
	})

	assert.Equal(t, "pinned", event["model"])
	assert.Equal(t, "pinned-response", event["response_model"])
}

// A harness naming no responding model must add no key at all, rather than an
// empty one: an empty gen_ai.response.model would shadow the request model for
// every reader that prefers the response and falls back only on absence.
func TestAttachUsage_omitsAnAbsentResponseModel(t *testing.T) {
	event := map[string]any{}
	attachUsage(event, &copilot.Usage{InputTokens: 10, Model: "gpt"})

	assert.Equal(t, "gpt", event["model"])
	_, present := event["response_model"]
	assert.False(t, present, "an absent responding model must add no key")
}
