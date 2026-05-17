// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PKCE holds the values generated for one authorization-code-grant exchange.
type PKCE struct {
	Verifier  string // 43-char base64url-encoded random
	Challenge string // S256(Verifier)
	State     string // CSRF protection
}

// GeneratePKCE creates a fresh verifier, challenge, and state per RFC 7636 + RFC 6749.
func GeneratePKCE() (*PKCE, error) {
	verifier, err := randBase64URL(32)
	if err != nil {
		return nil, fmt.Errorf("generating verifier: %w", err)
	}
	state, err := randBase64URL(16)
	if err != nil {
		return nil, fmt.Errorf("generating state: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return &PKCE{Verifier: verifier, Challenge: challenge, State: state}, nil
}

func randBase64URL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
