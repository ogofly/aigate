package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"aigate/internal/config"
)

type OpenAILikeProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func newOpenAILikeProvider(cfg config.ProviderConfig) (*OpenAILikeProvider, error) {
	return newOpenAILikeProviderWithBaseURLAndStream(cfg, cfg.BaseURL, false)
}

// NewOpenAILike creates an OpenAI-compatible provider from config.
func NewOpenAILike(cfg config.ProviderConfig) (*OpenAILikeProvider, error) {
	return newOpenAILikeProvider(cfg)
}

func newOpenAILikeProviderWithStream(cfg config.ProviderConfig, stream bool) (*OpenAILikeProvider, error) {
	return newOpenAILikeProviderWithBaseURLAndStream(cfg, cfg.BaseURL, stream)
}

func newOpenAILikeProviderWithBaseURLAndStream(cfg config.ProviderConfig, base string, stream bool) (*OpenAILikeProvider, error) {
	resolved, err := resolveProviderConfig(cfg, base)
	if err != nil {
		return nil, err
	}
	return newOpenAILikeProviderFromResolved(resolved, stream), nil
}

func newOpenAILikeProviderFromResolved(cfg resolvedProviderConfig, stream bool) *OpenAILikeProvider {
	return &OpenAILikeProvider{
		baseURL: cfg.baseURL,
		apiKey:  cfg.apiKey,
		client:  newHTTPClient(cfg.timeout, stream),
	}
}

// OpenAILikeClient implements the OpenAI-compatible Chat, ChatStream, and Embed methods.
type OpenAILikeClient struct {
	mu        sync.Mutex
	providers map[openAILikeProviderKey]*OpenAILikeProvider
}

type openAILikeProviderKey struct {
	baseURL string
	apiKey  string
	timeout int64
	stream  bool
}

func (c *OpenAILikeClient) provider(cfg config.ProviderConfig, stream bool) (*OpenAILikeProvider, error) {
	resolved, err := resolveProviderConfig(cfg, cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	key := openAILikeProviderKey{
		baseURL: resolved.baseURL,
		apiKey:  resolved.apiKey,
		timeout: int64(resolved.timeout),
		stream:  stream,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.providers == nil {
		c.providers = make(map[openAILikeProviderKey]*OpenAILikeProvider)
	}
	if p, ok := c.providers[key]; ok {
		return p, nil
	}
	p := newOpenAILikeProviderFromResolved(resolved, stream)
	c.providers[key] = p
	return p, nil
}

func (c *OpenAILikeClient) Chat(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*OpenAIResponse, error) {
	p, err := c.provider(provider, false)
	if err != nil {
		return nil, err
	}
	payload := *req
	payload.Model = upstreamModel

	var out OpenAIResponse
	if err := p.doJSON(ctx, "/chat/completions", payload, "", &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (c *OpenAILikeClient) Responses(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*OpenAIResponse, error) {
	p, err := c.provider(provider, false)
	if err != nil {
		return nil, err
	}
	payload := *req
	payload.Model = upstreamModel

	var out OpenAIResponse
	if err := p.doJSON(ctx, "/responses", payload, "application/json", &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (c *OpenAILikeClient) ChatStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error) {
	p, err := c.provider(provider, true)
	if err != nil {
		return nil, err
	}
	payload := *req
	payload.Model = upstreamModel

	return p.doStream(ctx, "/chat/completions", payload)
}

func (c *OpenAILikeClient) ResponsesStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error) {
	p, err := c.provider(provider, true)
	if err != nil {
		return nil, err
	}
	payload := *req
	payload.Model = upstreamModel

	return p.doStream(ctx, "/responses", payload)
}

func (c *OpenAILikeClient) Embed(ctx context.Context, provider config.ProviderConfig, req EmbeddingRequest, upstreamModel string) (*EmbeddingResponse, error) {
	p, err := c.provider(provider, false)
	if err != nil {
		return nil, err
	}
	payload := make(EmbeddingRequest, len(req)+1)
	for k, v := range req {
		payload[k] = v
	}
	payload["model"] = upstreamModel

	var out EmbeddingResponse
	if err := p.doJSON(ctx, "/embeddings", payload, "", &out); err != nil {
		return nil, err
	}

	return &out, nil
}

func (p *OpenAILikeProvider) doJSON(ctx context.Context, path string, payload any, accept string, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if accept != "" {
		httpReq.Header.Set("Accept", accept)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := readUpstreamResponse(resp)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (p *OpenAILikeProvider) doStream(ctx context.Context, path string, payload any) (*StreamResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
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
