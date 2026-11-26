// Package config keeps what the connector knows: where Pawabase is, who it is (its key), and the address of the database it serves.
//
// The file is written with owner-only permissions. Run as root or Administrator (which a system service needs) it lives in a
// system-wide place; otherwise in the user's configuration folder. PAWABASE_CONNECTOR_CONFIG names another file.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
)

// Config is the connector's saved state.
type Config struct {
	Server      string `json:"server"`
	ConnectorID string `json:"connector_id"`
	Key         string `json:"key"`
	DatabaseURL string `json:"database_url"`
	Name        string `json:"name,omitempty"`
}

// ErrNotEnrolled is returned by Load when there is no configuration yet.
var ErrNotEnrolled = errors.New("this connector is not enrolled yet: run `pawabase-connector enroll --server URL --token TOKEN`")

// Path is where the configuration file lives.
func Path() string {
	if p := os.Getenv("PAWABASE_CONNECTOR_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Dir(), "config.json")
}

// Dir is the folder holding the configuration (and, when running as a service, the log).
func Dir() string {
	if p := os.Getenv("PAWABASE_CONNECTOR_CONFIG"); p != "" {
		return filepath.Dir(p)
	}
	if isPrivileged() {
		switch runtime.GOOS {
		case "windows":
			base := os.Getenv("ProgramData")
			if base == "" {
				base = `C:\ProgramData`
			}
			return filepath.Join(base, "pawabase-connector")
		case "darwin":
			return "/Library/Application Support/pawabase-connector"
		default:
			return "/etc/pawabase-connector"
		}
	}
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "pawabase-connector")
	}
	return "."
}

// Load reads the configuration. DATABASE_URL and PAWABASE_CONNECTOR_DATABASE_URL in the environment override the saved address.
func Load() (*Config, error) {
	raw, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotEnrolled
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", Path(), err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("read %s: %w", Path(), err)
	}
	if v := firstEnv("PAWABASE_CONNECTOR_DATABASE_URL", "DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
	}
	if cfg.Server == "" || cfg.Key == "" {
		return nil, ErrNotEnrolled
	}
	return &cfg, nil
}

// CheckWritable makes sure the configuration can be saved, before a one-time enrollment token is spent.
func CheckWritable() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %w (run as root/Administrator, or set PAWABASE_CONNECTOR_CONFIG)", Dir(), err)
	}
	probe := filepath.Join(Dir(), ".write-check")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return fmt.Errorf("cannot write in %s: %w (run as root/Administrator, or set PAWABASE_CONNECTOR_CONFIG)", Dir(), err)
	}
	return os.Remove(probe)
}

// Save writes the configuration with owner-only permissions.
func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", Dir(), err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, Path())
}

// RedactedDatabase is the database address with its password hidden.
func (c *Config) RedactedDatabase() string {
	return Redact(c.DatabaseURL)
}

// Redact hides the password of a postgres:// address.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	return u.String()
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}
