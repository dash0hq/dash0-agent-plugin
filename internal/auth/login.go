// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// deriveCPAURL maps a Dash0-flavored OAuth host (regional API or Clerk)
// to its control-plane-api host. The CPA hosts the mint endpoint and
// middleware-info. Returns "" when authURL doesn't contain a ".dash0"
// hostname segment.
func deriveCPAURL(authURL string) string {
	_, suffix, ok := strings.Cut(authURL, ".dash0")
	if !ok {
		return ""
	}
	return "https://control-plane-api.dash0" + suffix
}

// LoginOptions controls the high-level Login orchestration.
type LoginOptions struct {
	// AuthURL is the OAuth authorization server root (e.g.
	// https://clerk.dash0.com). Required.
	AuthURL string
	// ClientID is the pre-registered OAuth client identifier. When empty
	// the plugin falls back to Dynamic Client Registration (RFC 7591)
	// against AuthURL's registration_endpoint — used by the test mock
	// and any Dash0-internal OAuth server that supports DCR. Clerk does
	// not support DCR, so production logins must supply ClientID.
	ClientID string
	// Scope is the OAuth scope string included in the authorize and
	// token requests. Defaults to "" (meaning "no scope param sent"),
	// which works for Dash0's native OAuth. For Clerk, callers should
	// pass "openid email profile offline_access".
	Scope string
	// ClientName / ClientURI are sent during Dynamic Client Registration
	// (when ClientID is empty).
	ClientName string
	ClientURI  string
	// CallbackTimeout bounds the wait for the redirect.
	CallbackTimeout time.Duration
	// Stdout / Stderr for progress output.
	Stdout io.Writer
	Stderr io.Writer
}

// LoginResult contains the persisted credentials produced by a successful run.
type LoginResult struct {
	Credentials  Credentials
	Organization *OrganizationInfo
}

// Login runs the full PKCE flow: discover, (register if needed), open
// browser, exchange code, optionally mint a long-lived machine token,
// persist credentials.
func Login(ctx context.Context, opts LoginOptions) (*LoginResult, error) {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.CallbackTimeout == 0 {
		opts.CallbackTimeout = 5 * time.Minute
	}
	if opts.AuthURL == "" {
		return nil, fmt.Errorf("auth URL is required")
	}

	fmt.Fprintf(opts.Stdout, "dash0: signing in to %s\n", opts.AuthURL)

	meta, err := DiscoverMetadata(ctx, opts.AuthURL)
	if err != nil {
		return nil, fmt.Errorf("OAuth discovery: %w", err)
	}

	clients, err := LoadClients()
	if err != nil {
		return nil, err
	}
	clientKey := opts.AuthURL
	clientEntry, exists := clients.Clients[clientKey]

	// When a pre-registered ClientID is supplied (Clerk path), override
	// whatever's cached in clients.json for this AuthURL.
	if opts.ClientID != "" && clientEntry.ClientID != opts.ClientID {
		clientEntry = ClientEntry{ClientID: opts.ClientID, Port: clientEntry.Port}
		exists = true
	}

	server, err := StartCallbackServer(clientEntry.Port)
	if err != nil {
		return nil, err
	}
	if exists && clientEntry.Port != 0 && server.Port() != clientEntry.Port {
		// Stored port was taken; we got a random one instead. The stored
		// redirect_uri no longer matches, so we need a fresh registration
		// (only possible when DCR is available).
		exists = opts.ClientID != ""
	}
	if !exists {
		if meta.RegistrationEndpoint == "" {
			return nil, fmt.Errorf("auth server does not support Dynamic Client Registration and no ClientID was provided")
		}
		clientID, err := RegisterClient(ctx, meta.RegistrationEndpoint, opts.ClientName, opts.ClientURI, server.URL)
		if err != nil {
			return nil, err
		}
		clientEntry = ClientEntry{ClientID: clientID, Port: server.Port()}
		fmt.Fprintf(opts.Stdout, "dash0: registered OAuth client (id %s)\n", clientID)
	}
	// Persist whichever client (DCR or pre-registered) we ended up using,
	// along with the bound port so the next login reuses the same
	// redirect_uri.
	clientEntry.Port = server.Port()
	clients.Clients[clientKey] = clientEntry
	if err := SaveClients(clients); err != nil {
		return nil, err
	}

	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	authorizeURL := BuildAuthorizeURL(meta, clientEntry.ClientID, server.URL, pkce, opts.Scope)

	fmt.Fprintf(opts.Stdout, "dash0: opening browser to complete sign-in...\n")
	fmt.Fprintf(opts.Stdout, "dash0: if it does not open, visit: %s\n", authorizeURL)
	fmt.Fprintf(opts.Stdout, "dash0: new to Dash0? Click 'Sign up' on the next page to start a free trial.\n")
	if err := OpenBrowser(authorizeURL); err != nil {
		fmt.Fprintf(opts.Stderr, "dash0: could not open browser automatically: %v\n", err)
	}

	cb, err := server.Wait(ctx, opts.CallbackTimeout)
	if err != nil {
		return nil, err
	}
	if cb.Error != "" {
		return nil, fmt.Errorf("authorization failed: %s: %s", cb.Error, cb.ErrorDescription)
	}
	if cb.State != pkce.State {
		return nil, fmt.Errorf("state mismatch on OAuth callback — refusing to continue")
	}
	if cb.Code == "" {
		return nil, fmt.Errorf("OAuth callback did not include an authorization code")
	}

	tok, err := ExchangeCodeForToken(ctx, meta.TokenEndpoint, clientEntry.ClientID, server.URL, cb.Code, pkce.Verifier)
	if err != nil {
		return nil, err
	}

	mintURL := deriveCPAURL(opts.AuthURL)
	if mintURL == "" {
		mintURL = opts.AuthURL
	}

	// Try to mint a long-lived auth_* token using the access_token. Works
	// when the access_token is a real JWT (Clerk OAuth provider) since the
	// CPA's CheckUserAuth accepts JWTs only. If mint fails, fall back to
	// using the access_token directly for OTLP ingest.
	authToken := tok.AccessToken
	if minted, err := MintMachineToken(ctx, mintURL, tok.AccessToken, "Dash0 Claude Code Plugin — auto-generated"); err == nil {
		authToken = minted
		fmt.Fprintf(opts.Stdout, "dash0: minted long-lived ingestion token\n")
	} else if errors.Is(err, ErrNotAdmin) {
		return nil, fmt.Errorf("You need admin access to mint an API token. Ask your organization admin to generate one and set DASH0_AUTH_TOKEN=<token>.")
	} else {
		fmt.Fprintf(opts.Stderr, "dash0: could not mint long-lived token (%v); falling back to the short-lived access token (refresh-token rotation will kick in automatically).\n", err)
	}

	orgInfo, _ := FetchOrganizationInfo(ctx, mintURL, tok.AccessToken)
	ingressURL := ""
	if orgInfo != nil && orgInfo.IngressURL != "" {
		ingressURL = orgInfo.IngressURL
	}

	orgID := tok.OrganizationTechnicalID
	if orgID == "" && orgInfo != nil {
		orgID = orgInfo.TechnicalID
	}

	creds := Credentials{
		AuthToken:               authToken,
		RefreshToken:            tok.RefreshToken,
		OrganizationTechnicalID: orgID,
		AuthURL:                 opts.AuthURL,
		ClientID:                clientEntry.ClientID,
		IngressURL:              ingressURL,
	}
	if err := SaveCredentials(&creds); err != nil {
		return nil, err
	}
	path, _ := credentialsPath()
	fmt.Fprintf(opts.Stdout, "dash0: signed in to org %s. Token saved to %s\n", orgID, path)
	return &LoginResult{Credentials: creds, Organization: orgInfo}, nil
}
