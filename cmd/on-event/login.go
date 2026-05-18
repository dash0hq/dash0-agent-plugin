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
	// Clerk OAuth provider hosts. clerk.dash0.com is verified to expose
	// RFC 8414 OAuth metadata + RS256-signed JWTs. The dev host follows
	// the same naming pattern (DNS may need to be set up).
	defaultAuthURL    = "https://clerk.dash0.com"
	defaultAuthURLDev = "https://clerk.dash0-dev.com"
	// defaultScope is what we send on the authorize request. openid
	// makes Clerk issue an id_token-style JWT (passes CPA CheckUserAuth),
	// email+profile populate claims, offline_access yields a refresh
	// token.
	defaultScope = "openid email profile offline_access"
)

func runLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	authURLFlag := fs.String("auth-url", "", "OAuth authorization server root (default: inferred from DASH0_OTLP_URL or DASH0_AUTH_URL, else "+defaultAuthURL+")")
	clientIDFlag := fs.String("client-id", "", "Pre-registered OAuth client_id (default: DASH0_OAUTH_CLIENT_ID env)")
	scopeFlag := fs.String("scope", "", "OAuth scope (default: "+defaultScope+")")
	timeout := fs.Duration("timeout", 5*time.Minute, "How long to wait for the browser redirect")
	if err := fs.Parse(args); err != nil {
		return err
	}

	authURL := resolveAuthURLForLogin(*authURLFlag)
	clientID := strings.TrimSpace(*clientIDFlag)
	if clientID == "" {
		clientID = strings.TrimSpace(os.Getenv("CLAUDE_PLUGIN_OPTION_OAUTH_CLIENT_ID"))
	}
	if clientID == "" {
		clientID = strings.TrimSpace(os.Getenv("DASH0_OAUTH_CLIENT_ID"))
	}
	scope := strings.TrimSpace(*scopeFlag)
	if scope == "" {
		scope = defaultScope
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := auth.LoginOptions{
		AuthURL:         authURL,
		ClientID:        clientID,
		Scope:           scope,
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
//  2. DASH0_AUTH_URL env
//  3. DASH0_OTLP_URL / CLAUDE_PLUGIN_OPTION_OTLP_URL hostname sniff
//     (.dash0-dev.com → dev Clerk, .dash0.com → prod Clerk)
//  4. defaultAuthURL (prod Clerk)
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
			switch {
			case strings.HasSuffix(host, ".dash0-dev.com"):
				return defaultAuthURLDev
			case strings.HasSuffix(host, ".dash0.com"):
				return defaultAuthURL
			}
		}
	}
	return defaultAuthURL
}
