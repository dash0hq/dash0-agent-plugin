// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Command demo generates mock Claude Code telemetry and sends it to Dash0.
//
// The same binary serves two roles:
//   - locally, it generates and sends turns once and exits (the CLI below);
//   - on AWS Lambda, it detects the runtime environment and serves the Lambda
//     Runtime API loop (see lambda.go), so the deploy script just compiles and
//     uploads this binary as `bootstrap` — no separate entry point or SDK.
//
// Usage:
//
//	go run ./cmd/demo -url https://ingress.eu-west-1.aws.dash0.com -token <auth> -dataset demo
//	DASH0_OTLP_URL=... DASH0_AUTH_TOKEN=... go run ./cmd/demo -n 25
//	go run ./cmd/demo -debug        # print payloads, send nothing
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/dash0hq/dash0-agent-plugin/internal/demo"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

func main() {
	url := flag.String("url", os.Getenv("DASH0_OTLP_URL"), "Dash0 OTLP ingress URL (or DASH0_OTLP_URL)")
	token := flag.String("token", os.Getenv("DASH0_AUTH_TOKEN"), "Dash0 auth token (or DASH0_AUTH_TOKEN)")
	dataset := flag.String("dataset", os.Getenv("DASH0_DATASET"), "Dash0 dataset (or DASH0_DATASET)")
	n := flag.Int("n", envInt("DEMO_TURNS", 1), "number of turns to generate and send (or DEMO_TURNS)")
	debug := flag.Bool("debug", false, "print OTLP payloads to stderr")
	flag.Parse()

	cfg := otlp.Config{
		OTLPUrl:   *url,
		AuthToken: *token,
		Dataset:   *dataset,
		Debug:     *debug,
	}

	// Running inside AWS Lambda's custom runtime: serve the Runtime API loop
	// instead of the one-shot CLI path.
	if os.Getenv("AWS_LAMBDA_RUNTIME_API") != "" {
		serveLambda(cfg, *n)
		return
	}

	if cfg.OTLPUrl == "" && !cfg.Debug {
		fmt.Fprintln(os.Stderr, "demo: no OTLP URL configured; pass -url/-token (or set DASH0_OTLP_URL) or use -debug")
		os.Exit(1)
	}

	ctx := context.Background()
	for i := 0; i < *n; i++ {
		if err := demo.Handle(ctx, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "demo: turn %d: %v\n", i+1, err)
			os.Exit(1)
		}
	}
	fmt.Fprintf(os.Stderr, "demo: sent %d turn(s)\n", *n)
}

// envInt returns the integer value of the env var, or fallback if unset/invalid.
func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}
