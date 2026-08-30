// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"errors"
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

	// Incidents first. They are small, and they are the only evidence that the
	// plugin was mute, so the breadcrumbs are not discarded until every one of
	// them is either sent or spooled — a kill mid-report must cost us nothing.
	incidents, commit, err := incident.Drain(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "on-event: reading plugin incidents: %v\n", err)
	}
	durable := true
	for i, inc := range incidents {
		// The same budget the spool drain gets. One report can spend the OTLP
		// client's whole timeout while the endpoint is down, so without this a
		// handful of distinct incidents would hold the user's hook for half a
		// minute. What is left stays claimed and goes out on a later invocation.
		if !time.Now().Before(deadline) {
			fmt.Fprintf(os.Stderr, "on-event: out of time, %d plugin incidents left for the next invocation\n", len(incidents)-i)
			durable = false
			break
		}
		err := otlp.SendPluginIncident(otlp.PluginIncident{
			Kind:      inc.Kind,
			Harness:   inc.Harness,
			Detail:    inc.Detail,
			SessionID: inc.SessionID,
			Count:     inc.Count,
			First:     inc.First,
			Last:      inc.Last,
		}, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "on-event: reporting plugin incident: %v\n", err)
		}
		// Spooled counts as safe: the payload is on disk and will go out later.
		if err != nil && !errors.Is(err, otlp.ErrSpooled) {
			durable = false
		}
	}
	if durable {
		commit()
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
			// Say which limit stopped the drain. "Still queued" on its own reads
			// like the endpoint failed again, which is a different problem from a
			// backlog that is simply larger than one invocation's budget.
			reason := "the endpoint failed again"
			switch {
			case !time.Now().Before(deadline):
				reason = "out of time"
			case sent >= backlogBatch:
				reason = "batch full"
			}
			fmt.Fprintf(os.Stderr, "on-event: sent %d spooled payloads, %d still queued (%s)\n", sent, left, reason)
		} else {
			fmt.Fprintf(os.Stderr, "on-event: sent %d spooled payloads\n", sent)
		}
	}
}
