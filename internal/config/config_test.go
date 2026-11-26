package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveLoadKeepsItPrivateAndLetsTheEnvironmentOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	t.Setenv("PAWABASE_CONNECTOR_CONFIG", path)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PAWABASE_CONNECTOR_DATABASE_URL", "")
	if _, err := Load(); err != ErrNotEnrolled {
		t.Fatalf("want ErrNotEnrolled before enrolling, got %v", err)
	}
	cfg := &Config{Server: "https://api.example.com", ConnectorID: "01J", Key: "pbcn_01J_secret", DatabaseURL: "postgres://u:p@db/app"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config must be owner-only, got %v", info.Mode().Perm())
		}
	}
	got, err := Load()
	if err != nil || got.Key != cfg.Key || got.DatabaseURL != cfg.DatabaseURL {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	t.Setenv("DATABASE_URL", "postgres://other:pw@db2/app")
	got, _ = Load()
	if got.DatabaseURL != "postgres://other:pw@db2/app" {
		t.Fatalf("DATABASE_URL should override the saved address, got %s", got.DatabaseURL)
	}
}

func TestRedactHidesOnlyThePassword(t *testing.T) {
	got := Redact("postgres://app:s3cret@db.internal:5432/app?sslmode=require")
	if got != "postgres://app:%2A%2A%2A@db.internal:5432/app?sslmode=require" {
		t.Fatalf("unexpected %s", got)
	}
	if Redact("postgres://app@db/app") != "postgres://app@db/app" {
		t.Fatal("an address without a password stays as it is")
	}
}
