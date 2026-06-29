// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package auth

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

// LoginOptions controls the high-level Login orchestration.
type LoginOptions struct {
	// AuthURL is the OAuth authorization server root (e.g.
	// https://api.eu-west-1.aws.dash0.com). Required.
	AuthURL string
	// ClientName / ClientURI are sent during Dynamic Client Registration.
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

	server, err := StartCallbackServer(clientEntry.Port)
	if err != nil {
		return nil, err
	}
	if exists && clientEntry.Port != 0 && server.Port() != clientEntry.Port {
		// Stored port was taken; got a random one. Must re-register since
		// Dash0's OAuth server enforces exact redirect_uri matching.
		exists = false
	}
	if !exists {
		if meta.RegistrationEndpoint == "" {
			return nil, fmt.Errorf("auth server does not support Dynamic Client Registration")
		}
		clientID, err := RegisterClient(ctx, meta.RegistrationEndpoint, opts.ClientName, opts.ClientURI, server.URL)
		if err != nil {
			return nil, err
		}
		clientEntry = ClientEntry{ClientID: clientID, Port: server.Port()}
		fmt.Fprintf(opts.Stdout, "dash0: registered OAuth client (id %s)\n", clientID)
	}
	clientEntry.Port = server.Port()
	clients.Clients[clientKey] = clientEntry
	if err := SaveClients(clients); err != nil {
		return nil, err
	}

	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	authorizeURL := BuildAuthorizeURL(meta, clientEntry.ClientID, server.URL, pkce, "")

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

	orgInfo, _ := FetchOrganizationInfo(ctx, opts.AuthURL, tok.AccessToken)
	ingressURL := ""
	if orgInfo != nil && orgInfo.IngressURL != "" {
		ingressURL = orgInfo.IngressURL
	}

	orgID := tok.OrganizationTechnicalID
	if orgID == "" && orgInfo != nil {
		orgID = orgInfo.TechnicalID
	}

	ingestionToken, err := ProvisionIngestionToken(ctx, opts.AuthURL, tok.AccessToken)
	if err != nil {
		fmt.Fprintf(opts.Stderr, "dash0: warning: could not provision ingestion token: %v\n", err)
	}

	creds := Credentials{
		AuthToken:               tok.AccessToken,
		IngestionToken:          ingestionToken,
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
