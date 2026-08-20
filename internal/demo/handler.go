// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package demo generates and sends mock Claude Code telemetry to Dash0. It
// simulates users exercising the agent plugin so the agent-monitoring views
// have realistic data to display. Each invocation produces exactly one agent
// turn (a chat span with a single child tool span) and exports it as OTLP.
//
// The package is transport-agnostic: Handle does the work and can be driven
// from a local main, a test, or an AWS Lambda wrapper. Actual cloud deployment
// is intentionally out of scope for now.
package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// Handle generates one random mock turn and exports it to the OTLP endpoint
// configured in cfg: the turn's chat + tool spans, plus the corresponding
// dash0.gen_ai.vcs.* metrics. Both are built from the same turn, so the metrics
// carry the same repository and branch as the spans.
//
// When canary is true, it additionally emits the deterministic cost-validation
// canary turns (one per model tier, fixed token usage; see canary.go). This is
// gated so the canary only lands in the cost-validation dataset and never
// pollutes the customer-facing demo datasets. Handle is the entry point shared
// by local invocation and the Lambda handler.
func Handle(ctx context.Context, cfg otlp.Config, canary bool) error {
	now := time.Now().UTC()

	if err := sendTurn(cfg, newTurn(now), now); err != nil {
		return err
	}

	if canary {
		for _, ct := range canaryTurns() {
			if err := sendTurn(cfg, ct, now); err != nil {
				return fmt.Errorf("canary turn (%s): %w", ct.user.Name, err)
			}
		}
	}
	return nil
}

// sendTurn exports one turn's chat + tool spans and its dash0.gen_ai.vcs.*
// metrics to the configured endpoint.
func sendTurn(cfg otlp.Config, t turn, now time.Time) error {
	req, err := t.traces(now)
	if err != nil {
		return fmt.Errorf("generating mock turn: %w", err)
	}
	if err := otlp.SendTracesRequest(req, cfg); err != nil {
		return fmt.Errorf("sending mock turn: %w", err)
	}
	if err := t.emitVCSMetrics(cfg, now); err != nil {
		return fmt.Errorf("sending VCS metrics: %w", err)
	}
	return nil
}
