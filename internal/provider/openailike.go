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
	"strings"
	"time"

	"aigate/internal/config"
)

type OpenAILikeProvider struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOpenAILike(cfg config.ProviderConfig) (*OpenAILikeProvider, error) {
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if _, err := url.Parse(baseURL); err != nil {
		return nil, fmt.Errorf("invalid base_url: %w", err)
	}

	timeout := 60 * time.Second
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	return &OpenAILikeProvider{
		name:    cfg.Name,
		baseURL: baseURL,
		apiKey:  cfg.APIKey,
		client: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

func (p *OpenAILikeProvider) Name() string {
	return p.name
}

func (p *OpenAILikeProvider) Chat(ctx context.Context, req *ChatRequest, upstreamModel string) (*ChatResponse, error) {
	payload := *req
	payload.Model = upstreamModel
	payload.Stream = false
	log.Printf("provider=%s op=chat stream=false upstream_model=%s", p.name, upstreamModel)

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
		log.Printf("provider=%s op=chat stream=false upstream_model=%s error=%v", p.name, upstreamModel, err)
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("provider=%s op=chat stream=false upstream_model=%s status=%d", p.name, upstreamModel, resp.StatusCode)
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out ChatResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &out, nil
}

func (p *OpenAILikeProvider) ChatStream(ctx context.Context, req *ChatRequest, upstreamModel string) (io.ReadCloser, error) {
	payload := *req
	payload.Model = upstreamModel
	payload.Stream = true
	log.Printf("provider=%s op=chat stream=true upstream_model=%s", p.name, upstreamModel)

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
		log.Printf("provider=%s op=chat stream=true upstream_model=%s error=%v", p.name, upstreamModel, err)
		return nil, fmt.Errorf("send request: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			log.Printf("provider=%s op=chat stream=true upstream_model=%s status=%d", p.name, upstreamModel, resp.StatusCode)
			return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
		}
		log.Printf("provider=%s op=chat stream=true upstream_model=%s status=%d", p.name, upstreamModel, resp.StatusCode)
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return resp.Body, nil
}

func (p *OpenAILikeProvider) Embed(ctx context.Context, req EmbeddingRequest, upstreamModel string) (*EmbeddingResponse, error) {
	payload := make(EmbeddingRequest, len(req)+1)
	for k, v := range req {
		payload[k] = v
	}
	payload["model"] = upstreamModel
	log.Printf("provider=%s op=embeddings upstream_model=%s", p.name, upstreamModel)

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
		log.Printf("provider=%s op=embeddings upstream_model=%s error=%v", p.name, upstreamModel, err)
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("provider=%s op=embeddings upstream_model=%s status=%d", p.name, upstreamModel, resp.StatusCode)
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var out EmbeddingResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &out, nil
}
