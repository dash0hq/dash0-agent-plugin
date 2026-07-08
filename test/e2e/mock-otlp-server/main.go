// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.
// SPDX-License-Identifier: Apache-2.0

// mock-otlp-server is a minimal HTTP server that records OTLP requests
// and exposes them via GET /requests for test assertions.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

type request struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Auth   string `json:"auth"`
	Size   int    `json:"bodySize"`
}

type state struct {
	mu       sync.Mutex
	requests []request
}

func main() {
	s := &state{}

	http.HandleFunc("/v1/traces", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, request{
			Method: r.Method,
			Path:   r.URL.Path,
			Auth:   r.Header.Get("Authorization"),
			Size:   len(body),
		})
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/requests", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		resp := map[string]any{
			"count":    len(s.requests),
			"requests": s.requests,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	fmt.Println("mock-otlp-server listening on :4319")
	_ = http.ListenAndServe(":4319", nil)
}
