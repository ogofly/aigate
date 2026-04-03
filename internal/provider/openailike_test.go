package provider_test

import (
	"testing"

	"aigate/internal/config"
	"aigate/internal/provider"
)

func TestNewOpenAILikeReadsAPIKeyFromEnvRef(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-secret")

	p, err := provider.NewOpenAILike(config.ProviderConfig{
		Name:           "openai",
		BaseURL:        "https://api.openai.com/v1",
		APIKeyRef:      "OPENAI_API_KEY",
		TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatalf("NewOpenAILike() error = %v", err)
	}
	if p.Name() != "openai" {
		t.Fatalf("Name() = %q, want %q", p.Name(), "openai")
	}
}
