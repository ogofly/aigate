package provider

import (
	"context"
	"io"
)

type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
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
