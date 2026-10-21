// Package config keeps the CLI's login state on disk.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Credentials are the tokens from a completed login. The CLI only ever sends
// them to APIURL.
type Credentials struct {
	APIURL       string    `json:"api_url"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Email        string    `json:"email,omitempty"`
	Org          string    `json:"org,omitempty"`
}

// PendingLogin is a device code waiting for the user to approve it in a browser.
type PendingLogin struct {
	APIURL          string    `json:"api_url"`
	DeviceCode      string    `json:"device_code"`
	UserCode        string    `json:"user_code"`
	VerificationURL string    `json:"verification_url"`
	ExpiresAt       time.Time `json:"expires_at"`
	Interval        int       `json:"interval"`
}

const (
	credentialsFile = "credentials.json"
	pendingFile     = "login-pending.json"
)

// Dir is where the CLI keeps its state: $XDG_CONFIG_HOME/nimbus, else
// ~/.config/nimbus.
func Dir(getenv func(string) string) (string, error) {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "nimbus"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "nimbus"), nil
}

// Store reads and writes the state files in Dir. Files are mode 0600 because
// they hold tokens. A missing or corrupt file reads as nil.
type Store struct{ Dir string }

func (s Store) LoadCredentials() *Credentials {
	var c Credentials
	if !s.load(credentialsFile, &c) {
		return nil
	}
	return &c
}

func (s Store) SaveCredentials(c *Credentials) error { return s.save(credentialsFile, c) }
func (s Store) DeleteCredentials() error             { return s.remove(credentialsFile) }

func (s Store) LoadPending() *PendingLogin {
	var p PendingLogin
	if !s.load(pendingFile, &p) {
		return nil
	}
	return &p
}

func (s Store) SavePending(p *PendingLogin) error { return s.save(pendingFile, p) }
func (s Store) DeletePending() error              { return s.remove(pendingFile) }

func (s Store) load(name string, v any) bool {
	b, err := os.ReadFile(filepath.Join(s.Dir, name))
	return err == nil && json.Unmarshal(b, v) == nil
}

func (s Store) save(name string, v any) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// Write a temp file and rename it, so a crash never leaves half a file.
	// CreateTemp makes the file 0600.
	f, err := os.CreateTemp(s.Dir, name+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(s.Dir, name))
}

func (s Store) remove(name string) error {
	err := os.Remove(filepath.Join(s.Dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
