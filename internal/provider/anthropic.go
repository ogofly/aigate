package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"llmgate/internal/config"
)

type AnthropicClient struct {
	mu        sync.Mutex
	providers map[anthropicProviderKey]*anthropicProvider
}

type anthropicProviderKey struct {
	baseURL string
	apiKey  string
	timeout int64
	stream  bool
	version string
}

func (c *AnthropicClient) provider(cfg config.ProviderConfig, stream bool) (*anthropicProvider, error) {
	if trimSpace(cfg.AnthropicBaseURL) == "" {
		return nil, fmt.Errorf("provider %q anthropic_base_url is required", cfg.Name)
	}
	resolved, err := resolveProviderConfig(cfg, cfg.AnthropicBaseURL)
	if err != nil {
		return nil, err
	}
	key := anthropicProviderKey{
		baseURL: resolved.baseURL,
		apiKey:  resolved.apiKey,
		timeout: int64(resolved.timeout),
		stream:  stream,
		version: anthropicVersion(cfg),
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.providers == nil {
		c.providers = make(map[anthropicProviderKey]*anthropicProvider)
	}
	if p, ok := c.providers[key]; ok {
		return p, nil
	}
	p := newAnthropicProviderFromResolved(resolved, stream)
	c.providers[key] = p
	return p, nil
}

func (c *AnthropicClient) Messages(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*AnthropicResponse, error) {
	p, err := c.provider(provider, false)
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

	respBody, err := readUpstreamResponse(resp)
	if err != nil {
		return nil, err
	}

	var out AnthropicResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

func (c *AnthropicClient) MessagesStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error) {
	p, err := c.provider(provider, true)
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
	resolved, err := resolveProviderConfig(cfg, base)
	if err != nil {
		return nil, err
	}
	return newAnthropicProviderFromResolved(resolved, stream), nil
}

func newAnthropicProviderFromResolved(cfg resolvedProviderConfig, stream bool) *anthropicProvider {
	return &anthropicProvider{
		baseURL: cfg.baseURL,
		apiKey:  cfg.apiKey,
		client:  newHTTPClient(cfg.timeout, stream),
	}
}
