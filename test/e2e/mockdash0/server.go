// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

// Package mockdash0 implements just enough of Dash0's OAuth and management
// API surface to drive the plugin's PKCE flow end-to-end in tests.
//
// Endpoints implemented:
//
//   - GET  /.well-known/oauth-authorization-server
//   - POST /oauth/register
//   - GET  /oauth/authorize   (auto-consents and 302s to redirect_uri)
//   - POST /oauth/token
//   - POST /public/ui/organization/auth-tokens
//   - GET  /public/ui/organization/me
//   - GET  /requests          (test-only: dumps captured requests)
package mockdash0

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

type CapturedRequest struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  string            `json:"query"`
	Auth   string            `json:"auth"`
	Body   string            `json:"body"`
	Form   map[string]string `json:"form,omitempty"`
}

type State struct {
	mu             sync.Mutex
	Requests       []CapturedRequest
	IngressURL     string // value returned by /organization/me
	OrgEndpoint404 bool
	FailMint       bool
	FailMintStatus int
}

func NewState() *State {
	return &State{}
}

func (s *State) CapturedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.Requests))
	for i, r := range s.Requests {
		out[i] = r.Path
	}
	return out
}

func (s *State) capture(r *http.Request) CapturedRequest {
	body, _ := io.ReadAll(r.Body)
	cr := CapturedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Auth:   r.Header.Get("Authorization"),
		Body:   string(body),
	}
	if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
		form, _ := url.ParseQuery(string(body))
		cr.Form = map[string]string{}
		for k, v := range form {
			if len(v) > 0 {
				cr.Form[k] = v[0]
			}
		}
	}
	s.mu.Lock()
	s.Requests = append(s.Requests, cr)
	s.mu.Unlock()
	return cr
}

// Handler returns the HTTP handler. baseURL is a callback because the
// concrete URL is only known after httptest.NewServer assigns one.
func Handler(s *State, baseURL func() string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		s.capture(r)
		b := baseURL()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 b,
			"authorization_endpoint": b + "/oauth/authorize",
			"token_endpoint":         b + "/oauth/token",
			"registration_endpoint":  b + "/oauth/register",
		})
	})

	mux.HandleFunc("/oauth/register", func(w http.ResponseWriter, r *http.Request) {
		s.capture(r)
		s.mu.Lock()
		id := fmt.Sprintf("mock-client-%d", len(s.Requests))
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": id})
	})

	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		s.capture(r)
		q := r.URL.Query()
		redirect := q.Get("redirect_uri")
		state := q.Get("state")
		if forceErr := q.Get("test_force_error"); forceErr != "" {
			http.Redirect(w, r, redirect+"?error="+forceErr+"&error_description=test+forced&state="+state, http.StatusFound)
			return
		}
		if q.Get("test_tamper_state") == "1" {
			http.Redirect(w, r, redirect+"?code=mock-code&state=tampered", http.StatusFound)
			return
		}
		http.Redirect(w, r, redirect+"?code=mock-code&state="+state, http.StatusFound)
	})

	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		cr := s.capture(r)
		if cr.Form["grant_type"] != "authorization_code" {
			http.Error(w, "unsupported_grant_type", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":              "dash0_at_mock",
			"token_type":                "Bearer",
			"expires_in":                900,
			"refresh_token":             "dash0_rt_mock",
			"scope":                     "*",
			"organization_technical_id": "mock-org",
		})
	})

	mux.HandleFunc("/public/ui/organization/auth-tokens", func(w http.ResponseWriter, r *http.Request) {
		s.capture(r)
		if s.FailMint {
			code := s.FailMintStatus
			if code == 0 {
				code = http.StatusInternalServerError
			}
			w.WriteHeader(code)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "auth_mock_machine_token"})
	})

	mux.HandleFunc("/public/ui/organization/me", func(w http.ResponseWriter, r *http.Request) {
		s.capture(r)
		if s.OrgEndpoint404 {
			http.NotFound(w, r)
			return
		}
		out := map[string]string{"technical_id": "mock-org"}
		if s.IngressURL != "" {
			out["ingress_url"] = s.IngressURL
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	mux.HandleFunc("/requests", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"count":    len(s.Requests),
			"requests": s.Requests,
		})
	})

	return mux
}
