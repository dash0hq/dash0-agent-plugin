// SPDX-FileCopyrightText: Copyright 2026 Dash0 Inc.

package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Credentials is the persisted state used by the OTLP hook to authenticate.
type Credentials struct {
	// AuthToken is the OAuth access_token (dash0_at_*) issued by the Dash0
	// regional API OAuth server. Refreshed on 401 via RefreshToken.
	AuthToken string `json:"auth_token"`
	// IngestionToken is the long-lived auth_* ingestion token provisioned via
	// PUT /api/auth-tokens/claude-code-plugin after a successful OAuth login.
	// When set, this is what the OTLP hook sends as the Bearer token instead
	// of AuthToken — auth_* tokens do not expire and avoid the OAuth token
	// rotation cycle for OTLP requests.
	IngestionToken string `json:"ingestion_token,omitempty"`
	// RefreshToken is used to obtain a fresh AuthToken when the current
	// one expires. Issued by the Dash0 OAuth server with the authorization
	// code grant.
	RefreshToken            string `json:"refresh_token,omitempty"`
	OrganizationTechnicalID string `json:"organization_technical_id,omitempty"`
	AuthURL                 string `json:"auth_url,omitempty"`
	// ClientID is the OAuth client we authenticated as. Needed for the
	// refresh_token grant since public clients identify themselves by
	// client_id alone.
	ClientID   string `json:"client_id,omitempty"`
	IngressURL string `json:"ingress_url,omitempty"`
}

// Clients is the persisted OAuth Dynamic Client Registration result, keyed
// by auth URL so users can sign in to multiple Dash0 environments
// (prod / dev) without re-registering each time.
type Clients struct {
	Clients map[string]ClientEntry `json:"clients"`
}

type ClientEntry struct {
	ClientID string `json:"client_id"`
	// Port is the loopback port registered as part of the redirect_uri.
	// Dash0's OAuth server enforces exact redirect_uri matching, so we
	// reuse the same port on every subsequent login for this client.
	Port int `json:"port,omitempty"`
}

// ConfigDir returns the directory under which dash0 stores its plugin
// credentials. On Linux/macOS this is $XDG_CONFIG_HOME/dash0 (or
// ~/.config/dash0); on Windows it's %APPDATA%\dash0. The directory is
// created with mode 0700 if missing.
func ConfigDir() (string, error) {
	if override := os.Getenv("DASH0_CONFIG_DIR"); override != "" {
		if err := os.MkdirAll(override, 0o700); err != nil {
			return "", fmt.Errorf("creating config dir: %w", err)
		}
		return override, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config dir: %w", err)
	}
	dir := filepath.Join(base, "dash0")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating config dir: %w", err)
	}
	return dir, nil
}

func credentialsPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

func clientsPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "clients.json"), nil
}

// LoadCredentials reads credentials.json. Returns (nil, nil) when the file
// does not exist — that's not an error, it just means the user hasn't logged
// in yet.
func LoadCredentials() (*Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing credentials: %w", err)
	}
	return &c, nil
}

// SaveCredentials writes credentials.json atomically with mode 0600.
func SaveCredentials(c *Credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	return writeJSONFile(path, c, 0o600)
}

// LoadClients reads clients.json. Returns an empty (but non-nil) Clients
// when the file does not exist.
func LoadClients() (*Clients, error) {
	path, err := clientsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Clients{Clients: map[string]ClientEntry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading clients: %w", err)
	}
	var c Clients
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing clients: %w", err)
	}
	if c.Clients == nil {
		c.Clients = map[string]ClientEntry{}
	}
	return &c, nil
}

// SaveClients writes clients.json with mode 0600.
func SaveClients(c *Clients) error {
	path, err := clientsPath()
	if err != nil {
		return err
	}
	return writeJSONFile(path, c, 0o600)
}

func writeJSONFile(path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}
