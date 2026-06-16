// Command demo generates mock Claude Code telemetry and sends it to Dash0.
//
// It is the locally-invokable driver for the demo handler. A future AWS Lambda
// wrapper can call internal/demo.Handle directly; this binary exists so the
// same path can be exercised from a developer machine.
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

	"github.com/dash0hq/dash0-agent-plugin/internal/demo"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

func main() {
	url := flag.String("url", os.Getenv("DASH0_OTLP_URL"), "Dash0 OTLP ingress URL (or DASH0_OTLP_URL)")
	token := flag.String("token", os.Getenv("DASH0_AUTH_TOKEN"), "Dash0 auth token (or DASH0_AUTH_TOKEN)")
	dataset := flag.String("dataset", os.Getenv("DASH0_DATASET"), "Dash0 dataset (or DASH0_DATASET)")
	n := flag.Int("n", 1, "number of turns to generate and send")
	debug := flag.Bool("debug", false, "print OTLP payloads to stderr")
	flag.Parse()

	cfg := otlp.Config{
		OTLPUrl:   *url,
		AuthToken: *token,
		Dataset:   *dataset,
		Debug:     *debug,
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
