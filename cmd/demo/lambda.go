// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/demo"
	"github.com/dash0hq/dash0-agent-plugin/internal/otlp"
)

// serveLambda implements the minimal subset of the AWS Lambda Runtime API that
// a provided.al2023 custom runtime needs: long-poll for the next invocation,
// run the handler, post the result, repeat. This is why the same `demo` binary
// works both as a local CLI and as the Lambda `bootstrap` — no SDK required.
//
// API reference: https://docs.aws.amazon.com/lambda/latest/dg/runtimes-api.html
func serveLambda(cfg otlp.Config, turns int) {
	base := "http://" + os.Getenv("AWS_LAMBDA_RUNTIME_API") + "/2018-06-01/runtime"
	// No client timeout: the /invocation/next poll blocks until work arrives.
	client := &http.Client{}

	if cfg.OTLPUrl == "" || cfg.AuthToken == "" {
		postString(client, base+"/init/error",
			`{"errorMessage":"DASH0_OTLP_URL and DASH0_AUTH_TOKEN must be set","errorType":"ConfigError"}`)
		os.Exit(1)
	}

	for {
		reqID, err := nextInvocation(client, base)
		if err != nil {
			fmt.Fprintf(os.Stderr, "demo: runtime next: %v\n", err)
			time.Sleep(time.Second) // avoid a hot loop on a transient API error
			continue
		}

		if err := runTurns(cfg, turns); err != nil {
			postString(client, base+"/invocation/"+reqID+"/error",
				fmt.Sprintf(`{"errorMessage":%q,"errorType":"HandlerError"}`, err.Error()))
			continue
		}
		postString(client, base+"/invocation/"+reqID+"/response",
			fmt.Sprintf(`{"sent":%d}`, turns))
	}
}

// nextInvocation blocks until the runtime delivers an event and returns its
// request id. The event payload itself is ignored — the schedule is the trigger.
func nextInvocation(client *http.Client, base string) (string, error) {
	resp, err := client.Get(base + "/invocation/next")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	reqID := resp.Header.Get("Lambda-Runtime-Aws-Request-Id")
	if reqID == "" {
		return "", fmt.Errorf("missing Lambda-Runtime-Aws-Request-Id header")
	}
	return reqID, nil
}

func runTurns(cfg otlp.Config, turns int) error {
	ctx := context.Background()
	for i := 0; i < turns; i++ {
		if err := demo.Handle(ctx, cfg); err != nil {
			return fmt.Errorf("turn %d/%d: %w", i+1, turns, err)
		}
	}
	return nil
}

func postString(client *http.Client, url, body string) {
	resp, err := client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: runtime post %s: %v\n", url, err)
		return
	}
	_ = resp.Body.Close()
}
