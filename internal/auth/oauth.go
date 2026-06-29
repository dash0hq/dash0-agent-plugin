// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Metadata is the subset of the OAuth Authorization Server Metadata
// (RFC 8414) the plugin needs.
type Metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

// TokenResponse mirrors the JSON body returned by /oauth/token.
type TokenResponse struct {
	AccessToken             string `json:"access_token"`
	TokenType               string `json:"token_type"`
	ExpiresIn               int    `json:"expires_in"`
	RefreshToken            string `json:"refresh_token"`
	Scope                   string `json:"scope"`
	OrganizationTechnicalID string `json:"organization_technical_id"`
}

const httpTimeout = 15 * time.Second

func httpClient() *http.Client {
	return &http.Client{Timeout: httpTimeout}
}

// DiscoverMetadata fetches /.well-known/oauth-authorization-server from the
// configured API base.
func DiscoverMetadata(ctx context.Context, apiBase string) (*Metadata, error) {
	u := strings.TrimRight(apiBase, "/") + "/.well-known/oauth-authorization-server"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return nil, fmt.Errorf("discovery: status %d: %s", resp.StatusCode, string(body))
	}
	var meta Metadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, fmt.Errorf("decoding discovery: %w", err)
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return nil, fmt.Errorf("discovery: missing endpoints in response")
	}
	return &meta, nil
}

// RegisterClient performs Dynamic Client Registration (RFC 7591) and
// returns the assigned client_id.
func RegisterClient(ctx context.Context, registrationEndpoint, clientName, clientURI, redirectURI string) (string, error) {
	body := map[string]any{
		"client_name":                clientName,
		"client_uri":                 clientURI,
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("registering client: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return "", fmt.Errorf("registration failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding registration response: %w", err)
	}
	if out.ClientID == "" {
		return "", fmt.Errorf("registration response did not include client_id")
	}
	return out.ClientID, nil
}

// BuildAuthorizeURL constructs the URL the browser opens. scope is
// optional; when empty no scope param is appended (Dash0 native OAuth
// uses "*" implicitly). For Clerk, pass "openid email profile offline_access".
func BuildAuthorizeURL(meta *Metadata, clientID, redirectURI string, pkce *PKCE, scope string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", pkce.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", pkce.State)
	if scope != "" {
		q.Set("scope", scope)
	}
	sep := "?"
	if strings.Contains(meta.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return meta.AuthorizationEndpoint + sep + q.Encode()
}

// RefreshCredentials uses the refresh_token in creds to obtain a fresh
// access_token and persists the updated credentials. Returns the updated
// Credentials on success.
//
// Callers should invoke this on 401 from an OTLP request. It assumes
// creds has AuthURL, ClientID, and RefreshToken populated.
func RefreshCredentials(ctx context.Context, creds *Credentials) (*Credentials, error) {
	if creds == nil {
		return nil, fmt.Errorf("no credentials loaded")
	}
	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh_token saved; user must re-run /dash0-agent-plugin:login")
	}
	if creds.AuthURL == "" || creds.ClientID == "" {
		return nil, fmt.Errorf("credentials missing auth_url or client_id; user must re-run /dash0-agent-plugin:login")
	}

	meta, err := DiscoverMetadata(ctx, creds.AuthURL)
	if err != nil {
		return nil, fmt.Errorf("OAuth discovery: %w", err)
	}

	tok, err := RefreshAccessToken(ctx, meta.TokenEndpoint, creds.ClientID, creds.RefreshToken)
	if err != nil {
		return nil, err
	}

	updated := *creds
	updated.AuthToken = tok.AccessToken
	if tok.RefreshToken != "" {
		updated.RefreshToken = tok.RefreshToken
	}
	if err := SaveCredentials(&updated); err != nil {
		return nil, fmt.Errorf("saving refreshed credentials: %w", err)
	}
	return &updated, nil
}

// RefreshAccessToken trades a refresh_token for a fresh access_token
// (and possibly rotated refresh_token). For public clients (token
// endpoint auth method "none"), client_id is required but no client
// secret is sent.
func RefreshAccessToken(ctx context.Context, tokenEndpoint, clientID, refreshToken string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return nil, fmt.Errorf("refresh failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}
	var tok TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, fmt.Errorf("decoding refresh response: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("refresh response missing access_token")
	}
	return &tok, nil
}

// ProvisionIngestionToken calls PUT /api/auth-tokens/claude-code-plugin with
// the OAuth access token and returns the long-lived auth_* ingestion token.
// This is a get-or-create call — repeated calls return the same token.
func ProvisionIngestionToken(ctx context.Context, apiBase, oauthToken string) (string, error) {
	u := strings.TrimRight(apiBase, "/") + "/api/auth-tokens/claude-code-plugin"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+oauthToken)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("provisioning ingestion token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return "", fmt.Errorf("provisioning ingestion token failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding ingestion token response: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("ingestion token response missing token field")
	}
	return out.Token, nil
}

// ExchangeCodeForToken trades the authorization code (plus the PKCE
// verifier) for an access + refresh token.
func ExchangeCodeForToken(ctx context.Context, tokenEndpoint, clientID, redirectURI, code, codeVerifier string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}
	var tok TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return nil, fmt.Errorf("decoding token response: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	return &tok, nil
}
