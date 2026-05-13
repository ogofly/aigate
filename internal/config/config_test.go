package config_test

import (
	"os"
	"path/filepath"
	"strings"
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

func TestValidateRejectsProviderURLsWithoutSchemeOrHost(t *testing.T) {
	tests := []struct {
		name     string
		provider config.ProviderConfig
		want     string
	}{
		{
			name: "relative base url",
			provider: config.ProviderConfig{
				Name:    "openai",
				BaseURL: "api.openai.com/v1",
				APIKey:  "secret",
			},
			want: "base_url must use http or https",
		},
		{
			name: "missing host",
			provider: config.ProviderConfig{
				Name:    "openai",
				BaseURL: "https:///v1",
				APIKey:  "secret",
			},
			want: "base_url must include a host",
		},
		{
			name: "invalid anthropic url",
			provider: config.ProviderConfig{
				Name:             "anthropic",
				BaseURL:          "https://api.example/v1",
				AnthropicBaseURL: "localhost:8080",
				APIKey:           "secret",
			},
			want: "anthropic_base_url must use http or https",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.provider.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %q, want containing %q", err.Error(), tt.want)
			}
		})
	}
}
