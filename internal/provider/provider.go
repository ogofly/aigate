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

// OpenAIResponse holds the raw JSON from an OpenAI-compatible upstream chat response.
type OpenAIResponse map[string]any

// AnthropicResponse holds the raw JSON from an Anthropic upstream messages response.
type AnthropicResponse map[string]any

type EmbeddingRequest map[string]any
type EmbeddingResponse map[string]any

type StreamResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

type Client interface {
	Chat(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*OpenAIResponse, error)
	ChatStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error)
	Messages(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*AnthropicResponse, error)
	MessagesStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error)
	Embed(ctx context.Context, provider config.ProviderConfig, req EmbeddingRequest, upstreamModel string) (*EmbeddingResponse, error)
}

// defaultClient combines OpenAI and Anthropic implementations to satisfy Client.
type defaultClient struct {
	openai    *OpenAILikeClient
	anthropic *AnthropicClient
}

func (c *defaultClient) Chat(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*OpenAIResponse, error) {
	return c.openai.Chat(ctx, provider, req, upstreamModel)
}

func (c *defaultClient) ChatStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error) {
	return c.openai.ChatStream(ctx, provider, req, upstreamModel)
}

func (c *defaultClient) Messages(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*AnthropicResponse, error) {
	return c.anthropic.Messages(ctx, provider, req, upstreamModel)
}

func (c *defaultClient) MessagesStream(ctx context.Context, provider config.ProviderConfig, req *ChatRequest, upstreamModel string) (*StreamResponse, error) {
	return c.anthropic.MessagesStream(ctx, provider, req, upstreamModel)
}

func (c *defaultClient) Embed(ctx context.Context, provider config.ProviderConfig, req EmbeddingRequest, upstreamModel string) (*EmbeddingResponse, error) {
	return c.openai.Embed(ctx, provider, req, upstreamModel)
}

// NewClient returns a neutral Client that supports both OpenAI-compatible and Anthropic protocols.
func NewClient() Client {
	return &defaultClient{
		openai:    &OpenAILikeClient{},
		anthropic: &AnthropicClient{},
	}
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
