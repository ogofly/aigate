package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"aigate/internal/config"
)

type AnthropicClient struct{}

func (c *AnthropicClient) Messages(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*AnthropicResponse, error) {
	p, err := newAnthropicProvider(provider, false)
	if err != nil {
		return nil, err
	}
	payload := *req
	payload.Model = upstreamModel

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion(provider))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, trimSpace(string(respBody)))
	}

	var out AnthropicResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

func (c *AnthropicClient) MessagesStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error) {
	p, err := newAnthropicProvider(provider, true)
	if err != nil {
		return nil, err
	}
	payload := *req
	payload.Model = upstreamModel

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion(provider))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	return &StreamResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       resp.Body,
	}, nil
}

// anthropicProvider represents a provider initialized with Anthropic-specific config.
type anthropicProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func newAnthropicProvider(cfg config.ProviderConfig, stream bool) (*anthropicProvider, error) {
	if trimSpace(cfg.AnthropicBaseURL) == "" {
		return nil, fmt.Errorf("provider %q anthropic_base_url is required", cfg.Name)
	}
	return newAnthropicProviderWithBaseURL(cfg, cfg.AnthropicBaseURL, stream)
}

func newAnthropicProviderWithBaseURL(cfg config.ProviderConfig, base string, stream bool) (*anthropicProvider, error) {
	baseURL := trimTrailingSlash(base)
	if err := validateAbsoluteHTTPURL(baseURL); err != nil {
		return nil, fmt.Errorf("invalid base_url: %w", err)
	}

	timeout := 60 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	apiKey := trimSpace(cfg.APIKey)
	if apiKey == "" && cfg.APIKeyRef != "" {
		apiKey = trimSpace(os.Getenv(cfg.APIKeyRef))
		if apiKey == "" {
			return nil, fmt.Errorf("provider %q api key env %q is empty", cfg.Name, cfg.APIKeyRef)
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf("provider %q requires api_key or api_key_ref", cfg.Name)
	}

	return &anthropicProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  newHTTPClient(timeout, stream),
	}, nil
}
