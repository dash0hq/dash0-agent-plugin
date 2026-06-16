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

// Handle generates exactly one mock turn and exports it to the OTLP endpoint
// configured in cfg. It is the entry point shared by local invocation and (in
// the future) the Lambda handler.
func Handle(ctx context.Context, cfg otlp.Config) error {
	req, err := GenerateTurn(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("generating mock turn: %w", err)
	}
	if err := otlp.SendTracesRequest(req, cfg); err != nil {
		return fmt.Errorf("sending mock turn: %w", err)
	}
	return nil
}
