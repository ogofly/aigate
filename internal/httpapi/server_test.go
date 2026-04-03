package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/httpapi"
	"aigate/internal/provider"
	"aigate/internal/router"
	"aigate/internal/usage"
)

type stubProvider struct {
	name        string
	lastModel   string
	lastChat    *provider.ChatRequest
	response    *provider.ChatResponse
	embedResp   *provider.EmbeddingResponse
	returnError error
	streamBody  string
	lastEmbed   provider.EmbeddingRequest
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Chat(_ context.Context, req *provider.ChatRequest, upstreamModel string) (*provider.ChatResponse, error) {
	s.lastModel = upstreamModel
	s.lastChat = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	return s.response, nil
}

func (s *stubProvider) ChatStream(_ context.Context, req *provider.ChatRequest, upstreamModel string) (io.ReadCloser, error) {
	s.lastModel = upstreamModel
	s.lastChat = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	return io.NopCloser(strings.NewReader(s.streamBody)), nil
}

func (s *stubProvider) Embed(_ context.Context, req provider.EmbeddingRequest, upstreamModel string) (*provider.EmbeddingResponse, error) {
	s.lastModel = upstreamModel
	s.lastEmbed = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	return s.embedResp, nil
}

func TestChatCompletionsRoutesToExpectedProviderModel(t *testing.T) {
	resp := provider.ChatResponse{
		"id": "chatcmpl-test",
	}
	p := &stubProvider{name: "openai", response: &resp}
	rt, err := router.New(map[string]provider.Provider{
		"openai": p,
	}, []config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini-upstream",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-app-001"}}), rt, usage.New(100))

	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	if p.lastModel != "gpt-4o-mini-upstream" {
		t.Fatalf("lastModel = %q, want %q", p.lastModel, "gpt-4o-mini-upstream")
	}
}

func TestModelsRequiresAuth(t *testing.T) {
	rt, err := router.New(map[string]provider.Provider{
		"openai": &stubProvider{name: "openai"},
	}, []config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-app-001"}}), rt, usage.New(100))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestModelsReturnsConfiguredModels(t *testing.T) {
	rt, err := router.New(map[string]provider.Provider{
		"openai": &stubProvider{name: "openai"},
	}, []config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini",
		},
		{
			PublicName:   "deepseek-chat",
			Provider:     "openai",
			UpstreamName: "deepseek-chat",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-app-001"}}), rt, usage.New(100))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(payload.Data) != 2 {
		t.Fatalf("len(data) = %d, want %d", len(payload.Data), 2)
	}
}

func TestChatCompletionsStream(t *testing.T) {
	p := &stubProvider{
		name:       "openai",
		streamBody: "data: {\"id\":\"chunk-1\"}\n\ndata: [DONE]\n\n",
	}
	rt, err := router.New(map[string]provider.Provider{
		"openai": p,
	}, []config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini-upstream",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-app-001"}}), rt, usage.New(100))
	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want %q", got, "text/event-stream")
	}
	if !strings.Contains(rr.Body.String(), "data: [DONE]") {
		t.Fatalf("body = %q, want stream payload", rr.Body.String())
	}
	if p.lastModel != "gpt-4o-mini-upstream" {
		t.Fatalf("lastModel = %q, want %q", p.lastModel, "gpt-4o-mini-upstream")
	}
}

func TestChatCompletionsStreamUpstreamError(t *testing.T) {
	p := &stubProvider{
		name:        "openai",
		returnError: errors.New("upstream failed"),
	}
	rt, err := router.New(map[string]provider.Provider{
		"openai": p,
	}, []config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini-upstream",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-app-001"}}), rt, usage.New(100))
	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
}

func TestEmbeddingsRoutesToExpectedProviderModel(t *testing.T) {
	resp := provider.EmbeddingResponse{
		"object": "list",
		"data": []map[string]any{
			{"embedding": []float64{0.1, 0.2}, "index": 0, "object": "embedding"},
		},
	}
	p := &stubProvider{name: "openai", embedResp: &resp}
	rt, err := router.New(map[string]provider.Provider{
		"openai": p,
	}, []config.ModelConfig{
		{
			PublicName:   "text-embedding-3-small",
			Provider:     "openai",
			UpstreamName: "text-embedding-3-small-upstream",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-app-001"}}), rt, usage.New(100))
	body := bytes.NewBufferString(`{"model":"text-embedding-3-small","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if p.lastModel != "text-embedding-3-small-upstream" {
		t.Fatalf("lastModel = %q, want %q", p.lastModel, "text-embedding-3-small-upstream")
	}
	if input, ok := p.lastEmbed["input"].(string); !ok || input != "hello" {
		t.Fatalf("input = %#v, want %q", p.lastEmbed["input"], "hello")
	}
}

func TestEmbeddingsRequiresModel(t *testing.T) {
	p := &stubProvider{name: "openai"}
	rt, err := router.New(map[string]provider.Provider{
		"openai": p,
	}, []config.ModelConfig{
		{
			PublicName:   "text-embedding-3-small",
			Provider:     "openai",
			UpstreamName: "text-embedding-3-small",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-app-001"}}), rt, usage.New(100))
	body := bytes.NewBufferString(`{"input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestUsageSummaryTracksChatTokens(t *testing.T) {
	resp := provider.ChatResponse{
		"id": "chatcmpl-test",
		"usage": map[string]any{
			"prompt_tokens":     float64(10),
			"completion_tokens": float64(5),
			"total_tokens":      float64(15),
		},
	}
	p := &stubProvider{name: "openai", response: &resp}
	rt, err := router.New(map[string]provider.Provider{
		"openai": p,
	}, []config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	recorder := usage.New(100)
	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-app-001", Name: "alice", Purpose: "debug"}}), rt, recorder)

	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	usageReq := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	usageReq.Header.Set("Authorization", "Bearer sk-app-001")
	usageRR := httptest.NewRecorder()
	handler.ServeHTTP(usageRR, usageReq)

	if usageRR.Code != http.StatusOK {
		t.Fatalf("usage status = %d, want %d", usageRR.Code, http.StatusOK)
	}

	var payload struct {
		Data struct {
			RequestCount   int64  `json:"request_count"`
			SuccessCount   int64  `json:"success_count"`
			TotalTokens    int64  `json:"total_tokens"`
			RequestTokens  int64  `json:"request_tokens"`
			ResponseTokens int64  `json:"response_tokens"`
			KeyName        string `json:"key_name"`
			Purpose        string `json:"purpose"`
		} `json:"data"`
	}
	if err := json.Unmarshal(usageRR.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload.Data.RequestCount != 1 || payload.Data.SuccessCount != 1 || payload.Data.TotalTokens != 15 {
		t.Fatalf("unexpected usage payload: %+v", payload.Data)
	}
	if payload.Data.RequestTokens != 10 || payload.Data.ResponseTokens != 5 {
		t.Fatalf("unexpected token payload: %+v", payload.Data)
	}
	if payload.Data.KeyName != "alice" || payload.Data.Purpose != "debug" {
		t.Fatalf("unexpected key metadata: %+v", payload.Data)
	}
}

func TestAdminUsageRequiresAdminKey(t *testing.T) {
	rt, err := router.New(map[string]provider.Provider{
		"openai": &stubProvider{name: "openai"},
	}, []config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-app-001"}}), rt, usage.New(100))
	req := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestAdminUsageReturnsAllSummaries(t *testing.T) {
	rt, err := router.New(map[string]provider.Provider{
		"openai": &stubProvider{name: "openai"},
	}, []config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	recorder := usage.New(100)
	recorder.Record(usage.NewRecord(auth.Principal{Key: "sk-user-1", Name: "user1"}, "chat.completions", "openai", "gpt-4o-mini", "gpt-4o-mini", true, 1, 2, 3, http.StatusOK, 0))
	recorder.Record(usage.NewRecord(auth.Principal{Key: "sk-user-2", Name: "user2"}, "embeddings", "openai", "text-embedding-3-small", "text-embedding-3-small", true, 2, 0, 2, http.StatusOK, 0))

	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-admin-001", Admin: true}}), rt, recorder)
	req := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	req.Header.Set("Authorization", "Bearer sk-admin-001")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("len(data) = %d, want %d", len(payload.Data), 2)
	}
}

func TestChatCompletionsPassesThroughUnknownFields(t *testing.T) {
	resp := provider.ChatResponse{"id": "chatcmpl-test"}
	p := &stubProvider{name: "openai", response: &resp}
	rt, err := router.New(map[string]provider.Provider{
		"openai": p,
	}, []config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := httpapi.New(auth.New([]config.KeyConfig{{Key: "sk-app-001"}}), rt, usage.New(100))
	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high","thinking":{"type":"enabled"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if p.lastChat == nil {
		t.Fatal("lastChat = nil")
	}
	if got, ok := p.lastChat.Extra["reasoning_effort"].(string); !ok || got != "high" {
		t.Fatalf("reasoning_effort = %#v, want %q", p.lastChat.Extra["reasoning_effort"], "high")
	}
	thinking, ok := p.lastChat.Extra["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %#v, want map", p.lastChat.Extra["thinking"])
	}
	if got, ok := thinking["type"].(string); !ok || got != "enabled" {
		t.Fatalf("thinking.type = %#v, want %q", thinking["type"], "enabled")
	}
}
