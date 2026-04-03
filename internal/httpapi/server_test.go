package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/httpapi"
	"aigate/internal/provider"
	"aigate/internal/router"
	"aigate/internal/store"
	"aigate/internal/usage"
)

type stubProvider struct {
	lastModel   string
	lastChat    *provider.ChatRequest
	response    *provider.ChatResponse
	embedResp   *provider.EmbeddingResponse
	returnError error
	streamBody  string
	lastEmbed   provider.EmbeddingRequest
}

func (s *stubProvider) Chat(_ context.Context, _ config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.ChatResponse, error) {
	s.lastModel = upstreamModel
	s.lastChat = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	return s.response, nil
}

func (s *stubProvider) ChatStream(_ context.Context, _ config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (io.ReadCloser, error) {
	s.lastModel = upstreamModel
	s.lastChat = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	return io.NopCloser(strings.NewReader(s.streamBody)), nil
}

func (s *stubProvider) Embed(_ context.Context, _ config.ProviderConfig, req provider.EmbeddingRequest, upstreamModel string) (*provider.EmbeddingResponse, error) {
	s.lastModel = upstreamModel
	s.lastEmbed = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	return s.embedResp, nil
}

func newHandler(t *testing.T, keys []config.KeyConfig, rt *router.Router, recorder *usage.Recorder, client provider.Client) http.Handler {
	t.Helper()
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "test-secret")
	if err := sqliteStore.SeedProvidersIfEmpty(context.Background(), []config.ProviderConfig{{
		Name:           "openai",
		BaseURL:        "https://api.openai.com/v1",
		APIKeyRef:      "OPENAI_API_KEY",
		TimeoutSeconds: 60,
	}}); err != nil {
		t.Fatalf("SeedProvidersIfEmpty() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	return httpapi.NewWithClient(auth.New(keys), config.AdminConfig{Username: "admin", Password: "pass"}, client, rt, recorder, sqliteStore, []string{"openai"})
}

func TestChatCompletionsRoutesToExpectedProviderModel(t *testing.T) {
	resp := provider.ChatResponse{
		"id": "chatcmpl-test",
	}
	p := &stubProvider{response: &resp}
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini-upstream",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)

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
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), &stubProvider{})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestModelsReturnsConfiguredModels(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
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

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), &stubProvider{})
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
	p := &stubProvider{streamBody: "data: {\"id\":\"chunk-1\"}\n\ndata: [DONE]\n\n"}
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini-upstream",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
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
	p := &stubProvider{returnError: errors.New("upstream failed")}
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini-upstream",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
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
	p := &stubProvider{embedResp: &resp}
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "text-embedding-3-small",
			Provider:     "openai",
			UpstreamName: "text-embedding-3-small-upstream",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
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
	p := &stubProvider{}
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "text-embedding-3-small",
			Provider:     "openai",
			UpstreamName: "text-embedding-3-small",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
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
	p := &stubProvider{response: &resp}
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	recorder := usage.New(100)
	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001", Name: "alice", Purpose: "debug"}}, rt, recorder, p)

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

func TestAdminUsageRequiresAdminSession(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), &stubProvider{})
	req := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAdminUsageReturnsAllSummaries(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	recorder := usage.New(100)
	recorder.Record(usage.NewRecord(auth.Principal{Key: "sk-user-1", Name: "user1"}, "chat.completions", "openai", "gpt-4o-mini", "gpt-4o-mini", true, 1, 2, 3, http.StatusOK, 0))
	recorder.Record(usage.NewRecord(auth.Principal{Key: "sk-user-2", Name: "user2"}, "embeddings", "openai", "text-embedding-3-small", "text-embedding-3-small", true, 2, 0, 2, http.StatusOK, 0))

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-user-001"}}, rt, recorder, &stubProvider{})

	loginBody := bytes.NewBufferString("username=admin&password=pass")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/usage", nil)
	req.AddCookie(cookies[0])
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
	p := &stubProvider{response: &resp}
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
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

func TestAdminKeysSaveReloadsAuth(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-bootstrap-001"}}, rt, usage.New(100), &stubProvider{})

	loginBody := bytes.NewBufferString("username=admin&password=pass")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)

	if loginRR.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want %d", loginRR.Code, http.StatusSeeOther)
	}
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	saveBody := bytes.NewBufferString("key=sk-user-001&name=user1&purpose=test")
	saveReq := httptest.NewRequest(http.MethodPost, "/admin/keys", saveBody)
	saveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	saveReq.AddCookie(cookies[0])
	saveRR := httptest.NewRecorder()
	handler.ServeHTTP(saveRR, saveReq)

	if saveRR.Code != http.StatusSeeOther {
		t.Fatalf("save status = %d, want %d", saveRR.Code, http.StatusSeeOther)
	}

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer sk-user-001")
	modelsRR := httptest.NewRecorder()
	handler.ServeHTTP(modelsRR, modelsReq)

	if modelsRR.Code != http.StatusOK {
		t.Fatalf("models status = %d, want %d", modelsRR.Code, http.StatusOK)
	}
}

func TestAdminKeysPageMasksKeyByDefault(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-bootstrap-001"}, {Key: "sk-user-001", Name: "user1"}}, rt, usage.New(100), &stubProvider{})

	loginBody := bytes.NewBufferString("username=admin&password=pass")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/admin/keys?"+url.Values{"flash": []string{"ok"}}.Encode(), nil)
	pageReq.AddCookie(cookies[0])
	pageRR := httptest.NewRecorder()
	handler.ServeHTTP(pageRR, pageReq)

	if pageRR.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", pageRR.Code, http.StatusOK)
	}
	body := pageRR.Body.String()
	if !strings.Contains(body, ">****<") {
		t.Fatalf("body missing masked key: %q", body)
	}
	if !strings.Contains(body, ">Show<") || !strings.Contains(body, ">Copy<") {
		t.Fatalf("body missing show/copy buttons: %q", body)
	}
}
