package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"aigate/internal/config"
)

func TestLoadResolvesSQLitePathRelativeToConfigFile(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "configs")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}

	configPath := filepath.Join(configDir, "config.json")
	content := []byte(`{
		"server": {"listen": ":8080"},
		"admin": {"username": "admin", "password": "pass"},
		"auth": {"keys": []},
		"storage": {"sqlite_path": "./data/sqlite.db", "flush_interval": 60}
	}`)
	if err := os.WriteFile(configPath, content, 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	want := filepath.Join(configDir, "data", "sqlite.db")
	if cfg.Storage.SQLitePath != want {
		t.Fatalf("SQLitePath = %q, want %q", cfg.Storage.SQLitePath, want)
	}
}
