package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"aigate/internal/config"
)

func TestLoadDotEnvSetsMissingValuesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := []byte("OPENAI_API_KEY=from-dotenv\nDEEPSEEK_API_KEY=deepseek-dotenv\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	t.Setenv("OPENAI_API_KEY", "from-env")
	if err := config.LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv() error = %v", err)
	}

	if got := os.Getenv("OPENAI_API_KEY"); got != "from-env" {
		t.Fatalf("OPENAI_API_KEY = %q, want %q", got, "from-env")
	}
	if got := os.Getenv("DEEPSEEK_API_KEY"); got != "deepseek-dotenv" {
		t.Fatalf("DEEPSEEK_API_KEY = %q, want %q", got, "deepseek-dotenv")
	}
}
