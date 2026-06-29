// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/internal/auth"
	"github.com/dash0hq/dash0-agent-plugin/internal/version"
)

const (
	// Dash0 regional OAuth server endpoints. The regional API hosts the
	// OAuth server, issues dash0_at_* tokens, and supports DCR (RFC 7591).
	defaultAuthURL    = "https://api.eu-west-1.aws.dash0.com"
	defaultAuthURLDev = "https://api.eu-west-1.aws.dash0-dev.com"
)

func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	authURLFlag := fs.String("auth-url", "", "OAuth authorization server root (default: inferred from OTLP_URL or DASH0_AUTH_URL, else "+defaultAuthURL+")")
	timeout := fs.Duration("timeout", 5*time.Minute, "How long to wait for the browser redirect")
	if err := fs.Parse(args); err != nil {
		return err
	}

	authURL := resolveAuthURLForLogin(*authURLFlag)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := auth.LoginOptions{
		AuthURL:         authURL,
		ClientName:      "Dash0 Claude Code Plugin",
		ClientURI:       "https://github.com/dash0hq/dash0-agent-plugin",
		CallbackTimeout: *timeout,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
	}
	if v := version.Version; v != "" {
		opts.ClientName = fmt.Sprintf("Dash0 Claude Code Plugin %s", v)
	}

	if _, err := auth.Login(ctx, opts); err != nil {
		return err
	}
	return nil
}

// resolveAuthURLForLogin picks the OAuth host based on (in order):
//  1. --auth-url flag
//  2. CLAUDE_PLUGIN_OPTION_AUTH_URL / DASH0_AUTH_URL env
//  3. OTLP_URL hostname: replaces "ingress." prefix with "api." to get the
//     regional API OAuth server (e.g. ingress.eu-west-1.aws.dash0.com →
//     api.eu-west-1.aws.dash0.com)
//  4. defaultAuthURL (EU prod)
func resolveAuthURLForLogin(flagValue string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("CLAUDE_PLUGIN_OPTION_AUTH_URL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("DASH0_AUTH_URL")); v != "" {
		return v
	}
	otlp := os.Getenv("CLAUDE_PLUGIN_OPTION_OTLP_URL")
	if otlp == "" {
		otlp = os.Getenv("DASH0_OTLP_URL")
	}
	if otlp != "" {
		if u, err := url.Parse(otlp); err == nil {
			host := u.Hostname()
			if strings.HasPrefix(host, "ingress.") {
				return "https://api." + strings.TrimPrefix(host, "ingress.")
			}
		}
	}
	return defaultAuthURL
}
