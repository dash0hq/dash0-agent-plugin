// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// opencode-v2-on-event is a persistent, serial V2 event consumer. The JavaScript
// plugin owns subscription and reload; this process owns correlation and OTLP.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/harness"
	"github.com/dash0hq/dash0-agent-plugin/internal/pipeline"
	"github.com/dash0hq/dash0-agent-plugin/internal/source/opencodev2"
)

func main() {
	// A shutting-down server signals its whole process group before the plugin
	// has delivered the last events: `opencode run --standalone` sends SIGTERM
	// ahead of session.execution.succeeded, so dying on it loses every turn's
	// chat span. Stdin EOF is the only end of input; the plugin's stop() still
	// SIGKILLs a consumer that outlives it.
	signal.Ignore(os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	if err := run(os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "opencode-v2-on-event: %v\n", err)
	}
}

func run(input io.Reader) error {
	h := harness.OpenCodeV2
	if !h.Enabled() {
		return nil
	}
	cfg := h.Config()
	if cfg.OTLPUrl == "" && !cfg.Debug {
		return nil
	}
	dir, err := h.DataDir()
	if err != nil {
		return err
	}
	if instance := os.Getenv("OPENCODE_V2_PLUGIN_INSTANCE"); instance != "" {
		if !pipeline.IsSafeSessionID(instance) {
			return fmt.Errorf("invalid plugin instance")
		}
		opencodev2.SweepInstances(dir, instance, time.Now())
		dir = filepath.Join(dir, instance)
	}
	p, err := opencodev2.Load(dir, cfg)
	if err != nil {
		return err
	}
	return eachLine(input, maxEventBytes, func(line []byte) {
		var event opencodev2.Event
		if err := json.Unmarshal(line, &event); err != nil {
			fmt.Fprintf(os.Stderr, "opencode-v2-on-event: invalid V2 event: %v\n", err)
			return
		}
		if err := p.Handle(event); err != nil {
			fmt.Fprintf(os.Stderr, "opencode-v2-on-event: %v\n", err)
		}
		if err := p.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "opencode-v2-on-event: saving V2 correlation state: %v\n", err)
		}
	})
}

// The plugin's own queue cap: no single event can legitimately be larger.
const maxEventBytes = 8 * 1024 * 1024

// eachLine calls handle for every newline-terminated line of input. A line over
// max is dropped and reading continues: this process lives for the whole
// server, so giving up on one oversized event must not lose every later one.
func eachLine(input io.Reader, max int, handle func([]byte)) error {
	r := bufio.NewReaderSize(input, 64*1024)
	var line []byte
	oversized := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !oversized {
			// Buffered up to max plus a \r\n terminator; the exact limit
			// applies to the payload once the line is complete.
			if len(line)+len(chunk) > max+2 {
				oversized, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		trimmed := bytes.TrimRight(line, "\r\n")
		if oversized || len(trimmed) > max {
			fmt.Fprintf(os.Stderr, "opencode-v2-on-event: dropped a V2 event over %d bytes\n", max)
		} else if len(trimmed) > 0 {
			handle(trimmed)
		}
		line, oversized = line[:0], false
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
