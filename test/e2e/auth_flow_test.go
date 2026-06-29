// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dash0hq/dash0-agent-plugin/test/e2e/mockdash0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// authorizeURLRE matches the line `dash0: if it does not open, visit: <URL>`
// printed by `on-event login` when DASH0_AUTH_NO_BROWSER=1 is set.
var authorizeURLRE = regexp.MustCompile(`https?://[^\s]+?/oauth/authorize\?[^\s]+`)

type authFixture struct {
	t          *testing.T
	binary     string
	configDir  string
	mockServer *httptest.Server
	mockState  *mockdash0.State
}

func setupAuth(t *testing.T) *authFixture {
	t.Helper()
	pluginDir := findPluginDir(t)
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "on-event")
	build := exec.Command("go", "build", "-o", binary, "./cmd/on-event")
	build.Dir = pluginDir
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build failed: %s", string(out))

	state := mockdash0.NewState()
	var srv *httptest.Server
	srv = httptest.NewServer(mockdash0.Handler(state, func() string { return srv.URL }))
	t.Cleanup(srv.Close)

	return &authFixture{
		t:          t,
		binary:     binary,
		configDir:  t.TempDir(),
		mockServer: srv,
		mockState:  state,
	}
}

func (f *authFixture) env(extra map[string]string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"DASH0_CONFIG_DIR=" + f.configDir,
		"DASH0_AUTH_NO_BROWSER=1",
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// runLogin starts `on-event login` and drives the browser side of the flow
// by HTTP-fetching the authorize URL once it appears on stdout. The mock
// 302s back to the binary's loopback callback, completing the round trip.
//
// urlMutator is called with the authorize URL the binary prints. It can
// return the URL unchanged or append query params (used by the tampered-
// state test).
func (f *authFixture) runLogin(env map[string]string, urlMutator func(string) string, args ...string) (stdout, stderr string, err error) {
	f.t.Helper()
	cmd := exec.Command(f.binary, append([]string{"login"}, args...)...)
	cmd.Env = f.env(env)
	stdoutPipe, _ := cmd.StdoutPipe()
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	require.NoError(f.t, cmd.Start())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		<-ctx.Done()
		if ctx.Err() == context.DeadlineExceeded {
			_ = cmd.Process.Kill()
		}
	}()

	var allOut bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		var seen bytes.Buffer
		buf := make([]byte, 4096)
		fired := false
		for {
			n, rerr := stdoutPipe.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
				allOut.Write(buf[:n])
				if !fired {
					if m := authorizeURLRE.FindString(seen.String()); m != "" {
						fired = true
						url := m
						if urlMutator != nil {
							url = urlMutator(url)
						}
						go func() {
							client := &http.Client{Timeout: 5 * time.Second}
							resp, e := client.Get(url)
							if e == nil && resp != nil {
								io.Copy(io.Discard, resp.Body)
								resp.Body.Close()
							}
						}()
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	werr := cmd.Wait()
	<-done
	return allOut.String(), errBuf.String(), werr
}

func (f *authFixture) credentials() map[string]any {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.configDir, "credentials.json"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(f.t, err)
	var out map[string]any
	require.NoError(f.t, json.Unmarshal(data, &out))
	return out
}

func (f *authFixture) clients() map[string]any {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.configDir, "clients.json"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(f.t, err)
	var out map[string]any
	require.NoError(f.t, json.Unmarshal(data, &out))
	return out
}

// 1 — fresh login from scratch.
func TestAuthFlow_NewUser(t *testing.T) {
	f := setupAuth(t)
	_, stderr, err := f.runLogin(nil, nil, "--auth-url", f.mockServer.URL)
	require.NoError(t, err, "stderr: %s", stderr)

	creds := f.credentials()
	require.NotNil(t, creds)
	assert.Equal(t, "dash0_at_mock", creds["auth_token"])
	assert.Equal(t, "auth_mock", creds["ingestion_token"])
	assert.Equal(t, "mock-org", creds["organization_technical_id"])
	assert.Equal(t, f.mockServer.URL, creds["auth_url"])

	cls := f.clients()
	require.NotNil(t, cls)
	cMap, _ := cls["clients"].(map[string]any)
	require.NotNil(t, cMap)
	entry, _ := cMap[f.mockServer.URL].(map[string]any)
	require.NotNil(t, entry, "client registration recorded under auth URL")
	assert.True(t, strings.HasPrefix(entry["client_id"].(string), "mock-client-"))

	info, _ := os.Stat(filepath.Join(f.configDir, "credentials.json"))
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// 2 — clients.json already exists for the auth URL; skip /oauth/register.
func TestAuthFlow_ExistingUserSkipsRegister(t *testing.T) {
	f := setupAuth(t)
	preseed := fmt.Sprintf(`{"clients":{%q:{"client_id":"pre-seeded-client"}}}`, f.mockServer.URL)
	require.NoError(t, os.WriteFile(filepath.Join(f.configDir, "clients.json"), []byte(preseed), 0o600))

	_, stderr, err := f.runLogin(nil, nil, "--auth-url", f.mockServer.URL)
	require.NoError(t, err, "stderr: %s", stderr)

	for _, r := range f.mockState.Requests {
		assert.NotEqual(t, "/oauth/register", r.Path, "register should be skipped when client.json exists")
	}
	assert.Equal(t, "dash0_at_mock", f.credentials()["auth_token"])
}

// 4 — tamper with `state` in callback; binary must reject.
func TestAuthFlow_StateMismatch(t *testing.T) {
	f := setupAuth(t)
	_, stderr, err := f.runLogin(nil,
		func(u string) string { return u + "&test_tamper_state=1" },
		"--auth-url", f.mockServer.URL)
	require.Error(t, err)
	assert.Contains(t, stderr, "state mismatch")
	assert.Nil(t, f.credentials())
}

// 5 — Dash0 returns ?error=access_denied via the mock's test_force_error.
func TestAuthFlow_UserDeniesConsent(t *testing.T) {
	f := setupAuth(t)
	_, stderr, err := f.runLogin(nil,
		func(u string) string { return u + "&test_force_error=access_denied" },
		"--auth-url", f.mockServer.URL)
	require.Error(t, err)
	assert.Contains(t, stderr, "access_denied")
	assert.Nil(t, f.credentials())
}

// 6 — /organization/me supplies the ingress URL; it lands in credentials.
func TestAuthFlow_OrgInfoSuppliesIngressURL(t *testing.T) {
	f := setupAuth(t)
	f.mockState.IngressURL = "https://ingress.eu-west-1.aws.dash0.com:4318"

	_, stderr, err := f.runLogin(nil, nil, "--auth-url", f.mockServer.URL)
	require.NoError(t, err, "stderr: %s", stderr)
	creds := f.credentials()
	require.NotNil(t, creds)
	assert.Equal(t, "https://ingress.eu-west-1.aws.dash0.com:4318", creds["ingress_url"])
}

// 7 — DASH0_AUTH_URL env supplies the OAuth host when --auth-url is absent.
func TestAuthFlow_AuthURLFromEnv(t *testing.T) {
	f := setupAuth(t)
	_, stderr, err := f.runLogin(
		map[string]string{"DASH0_AUTH_URL": f.mockServer.URL},
		nil,
	)
	require.NoError(t, err, "stderr: %s", stderr)
	creds := f.credentials()
	require.NotNil(t, creds)
	assert.Equal(t, f.mockServer.URL, creds["auth_url"])
}

// 8 — hook reads token from credentials.json when no env var is present.
func TestAuthFlow_HookReadsCredentialsFile(t *testing.T) {
	f := setupAuth(t)
	otlpSrv, requests := captureOTLP(t)
	creds := fmt.Sprintf(`{"auth_token":"auth_from_file","ingress_url":"%s"}`, otlpSrv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(f.configDir, "credentials.json"), []byte(creds), 0o600))

	cmd := exec.Command(f.binary)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"hook-test","model":"opus"}`)
	cmd.Env = f.env(map[string]string{
		"CLAUDE_PLUGIN_DATA":              t.TempDir(),
		"CLAUDE_PLUGIN_OPTION_OTLP_URL":   "",
		"CLAUDE_PLUGIN_OPTION_AUTH_TOKEN": "",
	})
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "binary output: %s", out)

	time.Sleep(200 * time.Millisecond)
	requests.mu.Lock()
	defer requests.mu.Unlock()
	require.NotEmpty(t, requests.list, "expected at least one OTLP request")
	assert.Equal(t, "Bearer auth_from_file", requests.list[0].auth)
}

// 9 — CLAUDE_PLUGIN_OPTION_AUTH_TOKEN wins over the file.
func TestAuthFlow_ConfigOptionOverridesFile(t *testing.T) {
	f := setupAuth(t)
	otlpSrv, requests := captureOTLP(t)
	creds := fmt.Sprintf(`{"auth_token":"auth_from_file","ingress_url":"%s"}`, otlpSrv.URL)
	require.NoError(t, os.WriteFile(filepath.Join(f.configDir, "credentials.json"), []byte(creds), 0o600))

	cmd := exec.Command(f.binary)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"override-test","model":"opus"}`)
	cmd.Env = f.env(map[string]string{
		"CLAUDE_PLUGIN_DATA":              t.TempDir(),
		"CLAUDE_PLUGIN_OPTION_AUTH_TOKEN": "explicit-token",
		"CLAUDE_PLUGIN_OPTION_OTLP_URL":   "",
	})
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "binary output: %s", out)

	time.Sleep(200 * time.Millisecond)
	requests.mu.Lock()
	defer requests.mu.Unlock()
	require.NotEmpty(t, requests.list)
	assert.Equal(t, "Bearer explicit-token", requests.list[0].auth)
}

// 10 — SessionStart hook with no token anywhere prints the login hint.
func TestAuthFlow_AutoPromptOnSessionStart(t *testing.T) {
	f := setupAuth(t)
	cmd := exec.Command(f.binary)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"prompt-test","model":"opus"}`)
	cmd.Env = f.env(map[string]string{
		"CLAUDE_PLUGIN_DATA": t.TempDir(),
	})
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	body := string(out)
	assert.Contains(t, body, "systemMessage")
	assert.Contains(t, body, "/dash0-agent-plugin:login")
}

// 11 — hook with an OTLP server that 401s prints the re-auth hint.
func TestAuthFlow_RevokedTokenHint(t *testing.T) {
	f := setupAuth(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	cmd := exec.Command(f.binary)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"revoked-test","model":"opus"}`)
	cmd.Env = f.env(map[string]string{
		"CLAUDE_PLUGIN_DATA":              t.TempDir(),
		"CLAUDE_PLUGIN_OPTION_OTLP_URL":   srv.URL,
		"CLAUDE_PLUGIN_OPTION_AUTH_TOKEN": "stale-token",
	})
	out, _ := cmd.CombinedOutput()
	body := string(out)
	assert.Contains(t, body, "auth token rejected")
	assert.Contains(t, body, "/dash0-agent-plugin:login")
}

type otlpRequests struct {
	mu   sync.Mutex
	list []capturedAuth
}

type capturedAuth struct {
	auth string
	path string
}

func captureOTLP(t *testing.T) (*httptest.Server, *otlpRequests) {
	t.Helper()
	reqs := &otlpRequests{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.mu.Lock()
		reqs.list = append(reqs.list, capturedAuth{auth: r.Header.Get("Authorization"), path: r.URL.Path})
		reqs.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, reqs
}
