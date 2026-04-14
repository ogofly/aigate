package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"aigate/internal/config"
)

type ChatRequest struct {
	Model  string         `json:"-"`
	Stream bool           `json:"-"`
	Raw    map[string]any `json:"-"`
}

type ChatResponse map[string]any
type EmbeddingRequest map[string]any
type EmbeddingResponse map[string]any

type StreamResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

type Client interface {
	Chat(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*ChatResponse, error)
	ChatStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error)
	Messages(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*ChatResponse, error)
	MessagesStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error)
	Embed(ctx context.Context, provider config.ProviderConfig, req EmbeddingRequest, upstreamModel string) (*EmbeddingResponse, error)
}

func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Raw = cloneRawMap(raw)
	if model, ok := raw["model"].(string); ok {
		r.Model = model
	}
	if stream, ok := raw["stream"].(bool); ok {
		r.Stream = stream
	}
	return nil
}

func (r ChatRequest) MarshalJSON() ([]byte, error) {
	payload := cloneRawMap(r.Raw)
	if payload == nil {
		payload = make(map[string]any, 2)
	}
	if r.Model != "" {
		payload["model"] = r.Model
	}
	if _, ok := payload["stream"]; !ok && r.Stream {
		payload["stream"] = true
	}
	return json.Marshal(payload)
}

func cloneRawMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
