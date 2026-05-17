// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPKCE_VerifierAndChallengeMatch(t *testing.T) {
	pkce, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE: %v", err)
	}
	if len(pkce.Verifier) < 43 {
		t.Fatalf("verifier too short: %d chars", len(pkce.Verifier))
	}
	sum := sha256.Sum256([]byte(pkce.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if pkce.Challenge != want {
		t.Fatalf("challenge != S256(verifier): got %q want %q", pkce.Challenge, want)
	}
	if pkce.State == "" {
		t.Fatal("state must not be empty")
	}
}

func TestPKCE_UniquePerCall(t *testing.T) {
	a, _ := GeneratePKCE()
	b, _ := GeneratePKCE()
	if a.Verifier == b.Verifier {
		t.Fatal("two GeneratePKCE calls produced the same verifier")
	}
	if a.State == b.State {
		t.Fatal("two GeneratePKCE calls produced the same state")
	}
}

func TestStorage_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DASH0_CONFIG_DIR", dir)

	if c, err := LoadCredentials(); err != nil || c != nil {
		t.Fatalf("expected (nil,nil) when file missing, got (%v,%v)", c, err)
	}

	creds := Credentials{
		AuthToken:               "auth_abc",
		OrganizationTechnicalID: "my-org",
		AuthURL:                 "https://control-plane-api.dash0.com",
		IngressURL:              "https://ingress.eu-west-1.aws.dash0.com:4318",
	}
	if err := SaveCredentials(&creds); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credentials.json mode = %v, want 0600", info.Mode().Perm())
	}

	loaded, err := LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if *loaded != creds {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", *loaded, creds)
	}
}

func TestStorage_ClientsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DASH0_CONFIG_DIR", dir)

	c, err := LoadClients()
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}
	if len(c.Clients) != 0 {
		t.Fatalf("expected empty clients, got %v", c.Clients)
	}

	prodURL := "https://control-plane-api.dash0.com"
	devURL := "https://control-plane-api.dash0-dev.com"
	c.Clients[prodURL] = ClientEntry{ClientID: "client-prod"}
	c.Clients[devURL] = ClientEntry{ClientID: "client-dev"}
	if err := SaveClients(c); err != nil {
		t.Fatalf("SaveClients: %v", err)
	}

	loaded, err := LoadClients()
	if err != nil {
		t.Fatalf("LoadClients reload: %v", err)
	}
	if loaded.Clients[prodURL].ClientID != "client-prod" || loaded.Clients[devURL].ClientID != "client-dev" {
		t.Fatalf("clients mismatch: %+v", loaded)
	}
}

func TestBuildAuthorizeURL(t *testing.T) {
	meta := &Metadata{AuthorizationEndpoint: "https://api.dash0.com/oauth/authorize"}
	pkce := &PKCE{Verifier: "v", Challenge: "ch", State: "st"}
	got := BuildAuthorizeURL(meta, "client123", "http://localhost:12345/callback", pkce, "")
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := parsed.Query()
	if q.Get("response_type") != "code" ||
		q.Get("client_id") != "client123" ||
		q.Get("redirect_uri") != "http://localhost:12345/callback" ||
		q.Get("code_challenge") != "ch" ||
		q.Get("code_challenge_method") != "S256" ||
		q.Get("state") != "st" {
		t.Fatalf("unexpected query in authorize URL: %s", got)
	}
}

func TestCallbackServer_ReceivesCode(t *testing.T) {
	srv, err := StartCallbackServer(0)
	if err != nil {
		t.Fatalf("StartCallbackServer: %v", err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = http.Get(srv.URL + "?code=the-code&state=the-state")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := srv.Wait(ctx, 2*time.Second)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != "the-code" || res.State != "the-state" {
		t.Fatalf("got %+v", res)
	}
}

func TestCallbackServer_Error(t *testing.T) {
	srv, err := StartCallbackServer(0)
	if err != nil {
		t.Fatalf("StartCallbackServer: %v", err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = http.Get(srv.URL + "?error=access_denied&error_description=user+said+no")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := srv.Wait(ctx, 2*time.Second)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Error != "access_denied" || res.ErrorDescription != "user said no" {
		t.Fatalf("got %+v", res)
	}
}

// mockDash0Server stands in for the Dash0 backend during oauth tests.
type mockDash0Server struct {
	server       *httptest.Server
	registered   atomic.Int32
	mintCalls    atomic.Int32
	failMint     bool
	failMintCode int
	omitOrgEp    bool
	overrideOrg  *OrganizationInfo
	overrideCode string
	codeIssued   atomic.Int32
}

func newMockDash0Server() *mockDash0Server {
	m := &mockDash0Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		base := m.server.URL
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 base,
			"authorization_endpoint": base + "/oauth/authorize",
			"token_endpoint":         base + "/oauth/token",
			"registration_endpoint":  base + "/oauth/register",
		})
	})
	mux.HandleFunc("/oauth/register", func(w http.ResponseWriter, r *http.Request) {
		m.registered.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"client_id": fmt.Sprintf("mock-client-%d", m.registered.Load()),
		})
	})
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		// Used by the e2e suite (auto-consent). Not exercised by these unit tests.
		http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?code=mock-code&state="+r.URL.Query().Get("state"), http.StatusFound)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		if m.overrideCode != "" && form.Get("code") != m.overrideCode {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:             "dash0_at_test",
			TokenType:               "Bearer",
			ExpiresIn:               900,
			Scope:                   "*",
			OrganizationTechnicalID: "mock-org",
		})
	})
	mux.HandleFunc("/public/ui/organization/auth-tokens", func(w http.ResponseWriter, r *http.Request) {
		m.mintCalls.Add(1)
		if m.failMint {
			code := m.failMintCode
			if code == 0 {
				code = http.StatusInternalServerError
			}
			w.WriteHeader(code)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "auth_test_machine_token"})
	})
	mux.HandleFunc("/public/ui/organization/me", func(w http.ResponseWriter, r *http.Request) {
		if m.omitOrgEp {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		info := OrganizationInfo{TechnicalID: "mock-org"}
		if m.overrideOrg != nil {
			info = *m.overrideOrg
		}
		_ = json.NewEncoder(w).Encode(info)
	})
	m.server = httptest.NewServer(mux)
	return m
}

func (m *mockDash0Server) Close() { m.server.Close() }

func TestDiscoverMetadata(t *testing.T) {
	m := newMockDash0Server()
	defer m.Close()
	meta, err := DiscoverMetadata(context.Background(), m.server.URL)
	if err != nil {
		t.Fatalf("DiscoverMetadata: %v", err)
	}
	if !strings.HasSuffix(meta.AuthorizationEndpoint, "/oauth/authorize") {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
}

func TestRegisterClient(t *testing.T) {
	m := newMockDash0Server()
	defer m.Close()
	id, err := RegisterClient(context.Background(), m.server.URL+"/oauth/register",
		"Test Plugin", "https://example.com", "http://localhost:1234/callback")
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	if !strings.HasPrefix(id, "mock-client-") {
		t.Fatalf("unexpected client id: %s", id)
	}
}

func TestMintMachineToken_NotAdmin(t *testing.T) {
	m := newMockDash0Server()
	defer m.Close()
	m.failMint = true
	m.failMintCode = http.StatusForbidden
	_, err := MintMachineToken(context.Background(), m.server.URL, "dash0_at_test", "desc")
	if err == nil || !errorIs(err, ErrNotAdmin) {
		t.Fatalf("expected ErrNotAdmin, got %v", err)
	}
}

func TestMintMachineToken_Success(t *testing.T) {
	m := newMockDash0Server()
	defer m.Close()
	token, err := MintMachineToken(context.Background(), m.server.URL, "dash0_at_test", "desc")
	if err != nil {
		t.Fatalf("MintMachineToken: %v", err)
	}
	if token != "auth_test_machine_token" {
		t.Fatalf("unexpected token: %s", token)
	}
}

func TestFetchOrganizationInfo_404IsNotError(t *testing.T) {
	m := newMockDash0Server()
	defer m.Close()
	m.omitOrgEp = true
	info, err := FetchOrganizationInfo(context.Background(), m.server.URL, "dash0_at_test")
	if err != nil {
		t.Fatalf("expected nil error on 404, got: %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil info on 404, got: %+v", info)
	}
}

// errorIs is a tiny shim that avoids importing errors in test code paths where
// we want behavioural matching against sentinel errors. Implemented inline so
// the test file compiles without extra deps.
func errorIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type wrapper interface{ Unwrap() error }
		if w, ok := err.(wrapper); ok {
			err = w.Unwrap()
			continue
		}
		return false
	}
	return false
}
