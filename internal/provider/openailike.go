package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

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
	baseURL := strings.TrimRight(base, "/")
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("invalid base_url: %w", err)
	}

	timeout := 60 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" && cfg.APIKeyRef != "" {
		apiKey = strings.TrimSpace(os.Getenv(cfg.APIKeyRef))
		if apiKey == "" {
			return nil, fmt.Errorf("provider %q api key env %q is empty", cfg.Name, cfg.APIKeyRef)
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf("provider %q requires api_key or api_key_ref", cfg.Name)
	}

	return &OpenAILikeProvider{
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  newHTTPClient(timeout, stream),
	}, nil
}

// OpenAILikeClient implements the OpenAI-compatible Chat, ChatStream, and Embed methods.
type OpenAILikeClient struct{}

func (c *OpenAILikeClient) Chat(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*OpenAIResponse, error) {
	p, err := newOpenAILikeProvider(provider)
	if err != nil {
		return nil, err
	}
	payload := *req
	payload.Model = upstreamModel
	log.Printf("provider=%s op=chat stream=false upstream_model=%s", provider.Name, upstreamModel)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		log.Printf("provider=%s op=chat stream=false upstream_model=%s error=%v", provider.Name, upstreamModel, err)
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("provider=%s op=chat stream=false upstream_model=%s status=%d", provider.Name, upstreamModel, resp.StatusCode)
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out OpenAIResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &out, nil
}

func (c *OpenAILikeClient) ChatStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error) {
	p, err := newOpenAILikeProviderWithStream(provider, true)
	if err != nil {
		return nil, err
	}
	payload := *req
	payload.Model = upstreamModel
	log.Printf("provider=%s op=chat stream=true upstream_model=%s", provider.Name, upstreamModel)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		log.Printf("provider=%s op=chat stream=true upstream_model=%s error=%v", provider.Name, upstreamModel, err)
		return nil, fmt.Errorf("send request: %w", err)
	}

	log.Printf(
		"provider=%s op=chat stream=true upstream_model=%s upstream_status=%d content_type=%q transfer_encoding=%q content_encoding=%q",
		provider.Name,
		upstreamModel,
		resp.StatusCode,
		resp.Header.Get("Content-Type"),
		strings.Join(resp.TransferEncoding, ","),
		resp.Header.Get("Content-Encoding"),
	)

	return &StreamResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       resp.Body,
	}, nil
}

func (c *OpenAILikeClient) Embed(ctx context.Context, provider config.ProviderConfig, req EmbeddingRequest, upstreamModel string) (*EmbeddingResponse, error) {
	p, err := newOpenAILikeProvider(provider)
	if err != nil {
		return nil, err
	}
	payload := make(EmbeddingRequest, len(req)+1)
	for k, v := range req {
		payload[k] = v
	}
	payload["model"] = upstreamModel
	log.Printf("provider=%s op=embeddings upstream_model=%s", provider.Name, upstreamModel)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		log.Printf("provider=%s op=embeddings upstream_model=%s error=%v", provider.Name, upstreamModel, err)
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("provider=%s op=embeddings upstream_model=%s status=%d", provider.Name, upstreamModel, resp.StatusCode)
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out EmbeddingResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &out, nil
}
