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
	if p == nil {
		t.Fatal("NewOpenAILike() returned nil provider")
	}
}

func TestNewOpenAILikePrefersDirectAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "from-env")

	p, err := provider.NewOpenAILike(config.ProviderConfig{
		Name:           "openai",
		BaseURL:        "https://api.openai.com/v1",
		APIKey:         "from-config",
		APIKeyRef:      "OPENAI_API_KEY",
		TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatalf("NewOpenAILike() error = %v", err)
	}
	if p == nil {
		t.Fatal("NewOpenAILike() returned nil provider")
	}
}
