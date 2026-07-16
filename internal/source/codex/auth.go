// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Billing modes emitted as dash0.codex.billing_mode. The cost the pipeline
// computes is always the OpenAI API list price × tokens. When Codex runs on a
// ChatGPT/Codex subscription (flat-rate, included usage) that figure is a
// list-price *equivalent*, not the user's actual spend — so we surface the
// billing mode and let the consumer label the cost accordingly.
const (
	billingSubscription = "subscription"
	billingAPI          = "api"
	billingUnknown      = "unknown"
)

// authPathOverride lets tests point at a fixture auth.json. Empty means the
// default ~/.codex/auth.json.
var authPathOverride string

func codexAuthPath() string {
	if authPathOverride != "" {
		return authPathOverride
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "auth.json")
}

// billingMode derives the Codex billing mode from the auth.json `auth_mode`
// field: Codex writes "chatgpt" for a ChatGPT/Codex subscription login and
// "apikey" for a pay-per-token API key (values verified against Codex 0.142.5;
// wire format is stable).
//
// Privacy: this file also holds the OpenAI API key and OAuth tokens. We decode
// ONLY auth_mode into a minimal struct — the secret fields are never mapped,
// logged, or emitted. Best-effort: any read/parse failure yields "unknown".
func billingMode(path string) string {
	if path == "" {
		return billingUnknown
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return billingUnknown
	}
	var a struct {
		AuthMode string `json:"auth_mode"`
	}
	if err := json.Unmarshal(data, &a); err != nil {
		return billingUnknown
	}
	switch a.AuthMode {
	case "":
		return billingUnknown
	case "chatgpt":
		return billingSubscription
	default: // "apikey" (or any other non-empty value) means a per-token API key
		return billingAPI
	}
}

// injectBillingMode stamps dash0.codex.billing_mode on the event (best-effort).
// The pipeline emits unmapped dash0.* keys verbatim, so this lands on the span.
func injectBillingMode(event map[string]any) {
	event["dash0.codex.billing_mode"] = billingMode(codexAuthPath())
}
