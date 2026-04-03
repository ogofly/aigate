package router_test

import (
	"testing"

	"aigate/internal/config"
	"aigate/internal/router"
)

func TestResolve(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
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
	rt, err := router.New([]config.ModelConfig{
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
