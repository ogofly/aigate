package provider

import (
	"testing"

	"aigate/internal/config"
)

func TestNewOpenAILikeReadsAPIKeyFromEnvRef(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-secret")

	p, err := NewOpenAILike(config.ProviderConfig{
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

	p, err := NewOpenAILike(config.ProviderConfig{
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

func TestNewHTTPClientKeepsTimeoutForNonStreamOnly(t *testing.T) {
	regular := newHTTPClient(3, false)
	if regular.Timeout != 3 {
		t.Fatalf("regular.Timeout = %v, want %v", regular.Timeout, 3)
	}

	stream := newHTTPClient(3, true)
	if stream.Timeout != 0 {
		t.Fatalf("stream.Timeout = %v, want %v", stream.Timeout, 0)
	}
}
