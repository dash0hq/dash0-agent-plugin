package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/dotenv"
	"github.com/dash0hq/dash0-agent-plugin/internal/filelog"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "on-event: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dotenv.Load(".env")

	dataDir := os.Getenv("CLAUDE_PLUGIN_DATA")
	if dataDir == "" {
		return fmt.Errorf("CLAUDE_PLUGIN_DATA is not set")
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("parsing JSON from stdin: %w", err)
	}

	now := time.Now().UTC()
	event["timestamp"] = now.Format(time.RFC3339Nano)

	if err := filelog.WriteEvent(event, dataDir); err != nil {
		return err
	}

	cfg := otlp.Config{
		OTLPUrl:   os.Getenv("DASH0_OTLP_URL"),
		AuthToken: os.Getenv("DASH0_AUTH_TOKEN"),
		Dataset:   os.Getenv("DASH0_DATASET"),
		AgentName: os.Getenv("DASH0_AGENT_NAME"),
	}
	if err := otlp.SendLog(event, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "on-event: otlp export: %v\n", err)
	}

	return nil
}
