package provider

import (
	"context"
	"encoding/json"
	"io"
)

type ChatRequest struct {
	Model       string         `json:"model"`
	Messages    []ChatMessage  `json:"messages"`
	Temperature *float64       `json:"temperature,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
	Extra       map[string]any `json:"-"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type ChatResponse map[string]any
type EmbeddingRequest map[string]any
type EmbeddingResponse map[string]any

type Provider interface {
	Name() string
	Chat(ctx context.Context, req *ChatRequest, upstreamModel string) (*ChatResponse, error)
	ChatStream(ctx context.Context, req *ChatRequest, upstreamModel string) (io.ReadCloser, error)
	Embed(ctx context.Context, req EmbeddingRequest, upstreamModel string) (*EmbeddingResponse, error)
}

func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	type alias ChatRequest
	var base alias
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	delete(raw, "model")
	delete(raw, "messages")
	delete(raw, "temperature")
	delete(raw, "stream")

	*r = ChatRequest(base)
	r.Extra = raw
	return nil
}

func (r ChatRequest) MarshalJSON() ([]byte, error) {
	payload := make(map[string]any, len(r.Extra)+4)
	for k, v := range r.Extra {
		payload[k] = v
	}

	payload["model"] = r.Model
	payload["messages"] = r.Messages
	if r.Temperature != nil {
		payload["temperature"] = *r.Temperature
	}
	if r.Stream {
		payload["stream"] = r.Stream
	}

	return json.Marshal(payload)
}
