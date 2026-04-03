package router_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"aigate/internal/config"
	"aigate/internal/provider"
	"aigate/internal/router"
)

type stubProvider struct {
	name string
}

func (s stubProvider) Name() string { return s.name }

func (s stubProvider) Chat(context.Context, *provider.ChatRequest, string) (*provider.ChatResponse, error) {
	resp := provider.ChatResponse{"ok": true}
	return &resp, nil
}

func (s stubProvider) ChatStream(context.Context, *provider.ChatRequest, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (s stubProvider) Embed(context.Context, provider.EmbeddingRequest, string) (*provider.EmbeddingResponse, error) {
	resp := provider.EmbeddingResponse{"ok": true}
	return &resp, nil
}

func TestResolve(t *testing.T) {
	rt, err := router.New(map[string]provider.Provider{
		"openai": stubProvider{name: "openai"},
	}, []config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	target, err := rt.Resolve("gpt-4o-mini")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if target.ProviderName != "openai" {
		t.Fatalf("ProviderName = %q, want %q", target.ProviderName, "openai")
	}

	if target.UpstreamModel != "gpt-4o-mini" {
		t.Fatalf("UpstreamModel = %q, want %q", target.UpstreamModel, "gpt-4o-mini")
	}
}

func TestResolveMissingModel(t *testing.T) {
	rt, err := router.New(map[string]provider.Provider{
		"openai": stubProvider{name: "openai"},
	}, []config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := rt.Resolve("missing-model"); err == nil {
		t.Fatal("Resolve() error = nil, want non-nil")
	}
}
