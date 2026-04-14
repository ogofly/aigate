package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"aigate/internal/config"
)

func TestNewOpenAILikeReadsAPIKeyFromEnvRef(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-secret")

	p, err := NewOpenAILike(config.ProviderConfig{
		Name:           "openai",
		BaseURL:        "https://api.openai.com/v1",
		APIKeyRef:      "OPENAI_API_KEY",
		TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatalf("NewOpenAILike() error = %v", err)
	}
	if p == nil {
		t.Fatal("NewOpenAILike() returned nil provider")
	}
}

func TestNewOpenAILikePrefersDirectAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "from-env")

	p, err := NewOpenAILike(config.ProviderConfig{
		Name:           "openai",
		BaseURL:        "https://api.openai.com/v1",
		APIKey:         "from-config",
		APIKeyRef:      "OPENAI_API_KEY",
		TimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatalf("NewOpenAILike() error = %v", err)
	}
	if p == nil {
		t.Fatal("NewOpenAILike() returned nil provider")
	}
}

func TestNewHTTPClientKeepsTimeoutForNonStreamOnly(t *testing.T) {
	regular := newHTTPClient(3, false)
	if regular.Timeout != 3 {
		t.Fatalf("regular.Timeout = %v, want %v", regular.Timeout, 3)
	}

	stream := newHTTPClient(3, true)
	if stream.Timeout != 0 {
		t.Fatalf("stream.Timeout = %v, want %v", stream.Timeout, 0)
	}
}

func TestChatUsesRawPayloadAndOnlyRewritesModel(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/chat/completions")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test"}`))
	}))
	defer srv.Close()

	client := NewOpenAILikeClient()
	reqBody := []byte(`{
		"model":"public-model",
		"messages":[{"role":"user","content":"hi","extra_in_message":{"foo":"bar"}}],
		"thinking":{"type":"enabled"},
		"custom_array":[1,2,3]
	}`)
	var req ChatRequest
	if err := json.Unmarshal(reqBody, &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	_, err := client.Chat(context.Background(), config.ProviderConfig{
		Name:           "openai",
		BaseURL:        srv.URL,
		APIKey:         "test-secret",
		TimeoutSeconds: 30,
	}, &req, "upstream-model")
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if got, _ := captured["model"].(string); got != "upstream-model" {
		t.Fatalf("model = %#v, want %q", captured["model"], "upstream-model")
	}
	if _, ok := captured["thinking"].(map[string]any); !ok {
		t.Fatalf("thinking = %#v, want map", captured["thinking"])
	}
	msgs, ok := captured["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("messages = %#v, want non-empty array", captured["messages"])
	}
	first, ok := msgs[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0] = %#v, want map", msgs[0])
	}
	if _, ok := first["extra_in_message"].(map[string]any); !ok {
		t.Fatalf("messages[0].extra_in_message = %#v, want map", first["extra_in_message"])
	}
	if _, exists := captured["stream"]; exists {
		t.Fatalf("stream should stay absent, got %#v", captured["stream"])
	}
}

func TestChatStreamPreservesRawStreamAndHeaders(t *testing.T) {
	var captured map[string]any
	var acceptHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptHeader = r.Header.Get("Accept")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	client := NewOpenAILikeClient()
	var req ChatRequest
	if err := json.Unmarshal([]byte(`{"model":"public-model","stream":true,"messages":[{"role":"user","content":"hi"}],"trace_id":"abc123"}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	resp, err := client.ChatStream(context.Background(), config.ProviderConfig{
		Name:           "openai",
		BaseURL:        srv.URL,
		APIKey:         "test-secret",
		TimeoutSeconds: 30,
	}, &req, "upstream-model")
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	_ = resp.Body.Close()

	if got, _ := captured["model"].(string); got != "upstream-model" {
		t.Fatalf("model = %#v, want %q", captured["model"], "upstream-model")
	}
	if got, _ := captured["stream"].(bool); !got {
		t.Fatalf("stream = %#v, want true", captured["stream"])
	}
	if got, _ := captured["trace_id"].(string); got != "abc123" {
		t.Fatalf("trace_id = %#v, want %q", captured["trace_id"], "abc123")
	}
	if acceptHeader != "text/event-stream" {
		t.Fatalf("accept header = %q, want %q", acceptHeader, "text/event-stream")
	}
}

func TestMessagesUsesAnthropicEndpointAndHeaders(t *testing.T) {
	var gotPath string
	var gotAPIKey string
	var gotVersion string
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer srv.Close()

	client := NewOpenAILikeClient()
	var req ChatRequest
	if err := json.Unmarshal([]byte(`{"model":"claude-public","messages":[{"role":"user","content":"hi"}]}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	_, err := client.Messages(context.Background(), config.ProviderConfig{
		Name:             "vendor",
		BaseURL:          "https://unused.example/v1",
		AnthropicBaseURL: srv.URL,
		AnthropicVersion: "2023-06-01",
		APIKey:           "test-secret",
		TimeoutSeconds:   30,
	}, &req, "claude-upstream")
	if err != nil {
		t.Fatalf("Messages() error = %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/messages")
	}
	if gotAPIKey != "test-secret" {
		t.Fatalf("x-api-key = %q, want %q", gotAPIKey, "test-secret")
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want %q", gotVersion, "2023-06-01")
	}
	if got, _ := captured["model"].(string); got != "claude-upstream" {
		t.Fatalf("model = %#v, want %q", captured["model"], "claude-upstream")
	}
}
