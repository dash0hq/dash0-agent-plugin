// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// CallbackResult is what the OAuth provider redirects to /callback with.
type CallbackResult struct {
	Code             string
	State            string
	Error            string
	ErrorDescription string
}

// CallbackServer is the loopback HTTP server that catches the redirect.
type CallbackServer struct {
	URL    string
	port   int
	srv    *http.Server
	result chan CallbackResult
}

// StartCallbackServer binds a localhost listener and returns a server
// ready to receive a single callback. When desiredPort != 0, the server
// binds to that exact port (required for repeat logins because Dash0's
// OAuth server enforces exact redirect_uri matching). When desiredPort
// == 0 (or binding the desired port fails) a random free port is used.
func StartCallbackServer(desiredPort int) (*CallbackServer, error) {
	if desiredPort != 0 {
		addr := fmt.Sprintf("127.0.0.1:%d", desiredPort)
		if listener, err := net.Listen("tcp", addr); err == nil {
			return newCallbackServer(listener), nil
		}
		// Fall through to random-port path; caller is responsible for
		// re-registering since the port-bound client is unusable here.
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			lastErr = err
			continue
		}
		return newCallbackServer(listener), nil
	}
	return nil, fmt.Errorf("could not bind loopback port after 3 attempts: %w", lastErr)
}

func newCallbackServer(listener net.Listener) *CallbackServer {
	port := listener.Addr().(*net.TCPAddr).Port
	cs := &CallbackServer{
		URL:    fmt.Sprintf("http://localhost:%d/callback", port),
		port:   port,
		result: make(chan CallbackResult, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", cs.handleCallback)
	cs.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = cs.srv.Serve(listener) }()
	return cs
}

// Port returns the loopback port the server is bound to.
func (s *CallbackServer) Port() int { return s.port }

func (s *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res := CallbackResult{
		Code:             q.Get("code"),
		State:            q.Get("state"),
		Error:            q.Get("error"),
		ErrorDescription: q.Get("error_description"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if res.Error != "" {
		fmt.Fprintf(w, callbackPage,
			"Sign-in failed",
			fmt.Sprintf("%s: %s", res.Error, res.ErrorDescription),
			"You can close this tab and return to your terminal.")
	} else if res.Code == "" {
		fmt.Fprintf(w, callbackPage,
			"Sign-in failed",
			"No authorization code received.",
			"You can close this tab and return to your terminal.")
	} else {
		fmt.Fprintf(w, callbackPage,
			"Sign-in complete",
			"You are now signed in to Dash0.",
			"You can close this tab and return to your terminal.")
	}
	select {
	case s.result <- res:
	default:
	}
}

// Wait blocks until a callback arrives or the timeout elapses, then
// shuts the server down.
func (s *CallbackServer) Wait(ctx context.Context, timeout time.Duration) (*CallbackResult, error) {
	defer s.shutdown()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-s.result:
		return &res, nil
	case <-timer.C:
		return nil, fmt.Errorf("timed out waiting for OAuth callback after %s", timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *CallbackServer) shutdown() {
	if s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

const callbackPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>%[1]s</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:6rem auto;padding:0 1rem;color:#222}h1{margin-bottom:.5rem}p{line-height:1.5}</style>
</head><body><h1>%[1]s</h1><p>%[2]s</p><p>%[3]s</p></body></html>
`
