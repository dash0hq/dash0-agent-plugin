// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/dash0hq/dash0-agent-plugin/internal/source/copilot"
)

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
// the turn than the file rollup is.
func TestAttachUsage_doesNotOverwriteEitherModel(t *testing.T) {
	event := map[string]any{"model": "pinned", "response_model": "pinned-response"}
	attachUsage(event, &copilot.Usage{
		InputTokens: 1,
		Model:       "auto", ResponseModel: "gpt-5.6-luna",
	})

	assert.Equal(t, "pinned", event["model"])
	assert.Equal(t, "pinned-response", event["response_model"])
}

// An empty gen_ai.response.model would shadow the request model for a reader
// that prefers the response and falls back only on absence.
func TestAttachUsage_omitsAnAbsentResponseModel(t *testing.T) {
	event := map[string]any{}
	attachUsage(event, &copilot.Usage{InputTokens: 10, Model: "gpt"})

	assert.Equal(t, "gpt", event["model"])
	_, present := event["response_model"]
	assert.False(t, present, "an absent responding model must add no key")
}
