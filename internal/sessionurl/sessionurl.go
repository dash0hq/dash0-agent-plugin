// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sessionurl builds the Dash0 app URL that points at a coding-agent
// session, so every source entrypoint can surface the same "view session" link.
package sessionurl

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
)

// SessionURL builds the Dash0 app URL that opens the given coding-agent session,
// mapping the OTLP ingress URL to its app host. The dataset (empty means the
// Dash0 default) is encoded into the URL state so the UI opens the session in
// the dataset the telemetry was sent to. It returns "" when the OTLP URL
// doesn't match a known Dash0 host (self-hosted / custom endpoints), which
// callers treat as "no link available".
func SessionURL(otlpURL, sessionID, dataset string) string {
	appURL := deriveAppURL(otlpURL)
	if appURL == "" {
		return ""
	}
	return buildSessionURL(appURL, sessionID, dataset)
}

// deriveAppURL maps an OTLP ingress URL to the corresponding Dash0 app URL.
// Returns empty string if the URL doesn't match a known Dash0 pattern.
func deriveAppURL(otlpURL string) string {
	if otlpURL == "" {
		return ""
	}
	u, err := url.Parse(otlpURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	switch {
	case strings.HasSuffix(host, ".dash0.com"):
		return "https://app.dash0.com"
	case strings.HasSuffix(host, ".dash0-dev.com"):
		return "https://app.dash0-dev.com"
	default:
		return ""
	}
}

// buildSessionURL constructs a full Dash0 AI Coding URL with the encoded URL
// state parameter that the Dash0 UI expects. It points at the sessions table
// on the /coding-agents page and sets agentSession so the UI auto-opens the
// session detail sidebar. A non-empty dataset is stored under the "/" pathname,
// matching the UI's global dataset URL binding; when empty the UI falls back
// to its default dataset.
func buildSessionURL(appURL, sessionID, dataset string) string {
	const codingAgentsPath = "/coding-agents"
	state := map[string]any{
		codingAgentsPath: map[string]any{
			"tab":          map[string]any{"pageTab": "sessions"},
			"agentSession": sessionID,
		},
	}
	if dataset != "" {
		state["/"] = map[string]any{"dataset": dataset}
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return appURL + codingAgentsPath
	}
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(stateJSON)
	_ = w.Close()
	encoded := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(buf.Bytes())
	return appURL + codingAgentsPath + "?s=" + encoded
}
