// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrNotAdmin is returned by MintMachineToken when the server replies 403.
var ErrNotAdmin = errors.New("not an organization admin")

// MintMachineToken calls POST /public/ui/organization/auth-tokens to mint
// a long-lived machine token. Returns ErrNotAdmin on 403.
func MintMachineToken(ctx context.Context, apiBase, accessToken, description string) (string, error) {
	u := strings.TrimRight(apiBase, "/") + "/public/ui/organization/auth-tokens"
	body, _ := json.Marshal(map[string]string{"description": description})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("minting machine token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return "", ErrNotAdmin
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return "", fmt.Errorf("mint token failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding mint response: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("mint response missing token field")
	}
	return out.Token, nil
}

// OrganizationInfo is the subset of org metadata we use to lock in the
// correct ingestion URL after login.
type OrganizationInfo struct {
	TechnicalID string `json:"technical_id"`
	IngressURL  string `json:"ingress_url"`
}

// FetchOrganizationInfo attempts to fetch the org's ingest URL.
// Returns (nil, nil) when the endpoint is unavailable (404) — older Dash0
// deployments may not expose this yet, in which case the caller must
// fall back to a user-supplied DASH0_OTLP_URL.
func FetchOrganizationInfo(ctx context.Context, apiBase, accessToken string) (*OrganizationInfo, error) {
	u := strings.TrimRight(apiBase, "/") + "/public/ui/organization/me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching organization info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Backend doesn't expose this endpoint; treat as "unknown" rather
		// than failing the whole login.
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return nil, fmt.Errorf("organization info failed (status %d): %s", resp.StatusCode, string(bodyBytes))
	}
	var info OrganizationInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decoding organization info: %w", err)
	}
	return &info, nil
}
