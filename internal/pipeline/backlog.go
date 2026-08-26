// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"fmt"
	"os"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/incident"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
	"github.com/dash0hq/dash0-agent-plugin/internal/spool"
)

const (
	// backlogBudget caps how long one invocation spends catching up. The hook is
	// on the user's critical path, so the backlog is drained a slice at a time
	// across invocations rather than all at once. It only applies to the backlog:
	// the current event's own send is unchanged.
	backlogBudget = 3 * time.Second

	// backlogBatch caps how many spooled payloads one invocation sends, so a
	// large backlog cannot monopolize a fast connection either.
	backlogBatch = 25
)

// flushBacklog ships what earlier invocations could not.
//
// Two things accumulate while the endpoint is unreachable: OTLP payloads whose
// send failed, and breadcrumbs from hooks that could not run at all. Both are on
// disk in the data root, and this invocation may be the first one with a working
// network, so it drains them before handling its own event — oldest first, so
// the order data arrives in matches the order it happened.
//
// Nothing here can fail loudly. A failure means the network is still down, which
// is the state this whole mechanism exists to survive.
func flushBacklog(cfg otlp.Config, dataDir string, now time.Time) {
	if cfg.OTLPUrl == "" || dataDir == "" {
		return
	}
	deadline := now.Add(backlogBudget)

	// Incidents first. They are small, they are the only evidence that the plugin
	// was mute, and a send failure re-spools them rather than dropping them.
	incidents, err := incident.Drain(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "on-event: reading plugin incidents: %v\n", err)
	}
	for _, inc := range incidents {
		if err := otlp.SendPluginIncident(otlp.PluginIncident{
			Kind:      inc.Kind,
			Harness:   inc.Harness,
			Detail:    inc.Detail,
			SessionID: inc.SessionID,
			Count:     inc.Count,
			First:     inc.First,
			Last:      inc.Last,
		}, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "on-event: reporting plugin incident: %v\n", err)
		}
	}

	// Then the spooled payloads. SendOnce, not the spooling send: a payload that
	// fails again belongs where it already is, not written a second time.
	dir := spool.Dir(dataDir)
	sent, err := spool.Drain(dir, backlogBatch, deadline, func(otlpPath string, payload []byte) error {
		return otlp.SendOnce(cfg, otlpPath, payload)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "on-event: draining the telemetry spool: %v\n", err)
	}
	if sent > 0 {
		if left := spool.Len(dir); left > 0 {
			fmt.Fprintf(os.Stderr, "on-event: sent %d spooled payloads, %d still queued\n", sent, left)
		} else {
			fmt.Fprintf(os.Stderr, "on-event: sent %d spooled payloads\n", sent)
		}
	}
}
