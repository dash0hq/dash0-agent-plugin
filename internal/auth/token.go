// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

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
