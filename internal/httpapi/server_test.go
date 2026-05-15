package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/httpapi"
	"aigate/internal/logger"
	"aigate/internal/provider"
	"aigate/internal/router"
	"aigate/internal/store"
	"aigate/internal/usage"
)

type stubProvider struct {
	lastModel          string
	lastChat           *provider.ChatRequest
	lastMessages       *provider.ChatRequest
	response           *provider.OpenAIResponse
	messagesResponse   *provider.AnthropicResponse
	embedResp          *provider.EmbeddingResponse
	returnError        error
	streamBody         string
	streamRC           io.ReadCloser
	streamResp         *provider.StreamResponse
	messagesStreamResp *provider.StreamResponse
	lastEmbed          provider.EmbeddingRequest
	chatErrors         []error
	chatCalls          int
}

func (s *stubProvider) Chat(_ context.Context, _ config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.OpenAIResponse, error) {
	s.lastModel = upstreamModel
	s.lastChat = req
	s.chatCalls++
	if len(s.chatErrors) > 0 {
		err := s.chatErrors[0]
		s.chatErrors = s.chatErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	if s.returnError != nil {
		return nil, s.returnError
	}
	return s.response, nil
}

func (s *stubProvider) ChatStream(_ context.Context, _ config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.StreamResponse, error) {
	s.lastModel = upstreamModel
	s.lastChat = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	if s.streamResp != nil {
		return s.streamResp, nil
	}
	if s.streamRC != nil {
		return &provider.StreamResponse{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       s.streamRC,
		}, nil
	}
	return &provider.StreamResponse{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(s.streamBody)),
	}, nil
}

func (s *stubProvider) Messages(_ context.Context, _ config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.AnthropicResponse, error) {
	s.lastModel = upstreamModel
	s.lastMessages = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	return s.messagesResponse, nil
}

func (s *stubProvider) MessagesStream(_ context.Context, _ config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.StreamResponse, error) {
	s.lastModel = upstreamModel
	s.lastMessages = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	if s.messagesStreamResp != nil {
		return s.messagesStreamResp, nil
	}
	if s.streamResp != nil {
		return s.streamResp, nil
	}
	if s.streamRC != nil {
		return &provider.StreamResponse{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       s.streamRC,
		}, nil
	}
	return &provider.StreamResponse{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(s.streamBody)),
	}, nil
}

type chunkedReadCloser struct {
	chunks []string
	index  int
}

func (r *chunkedReadCloser) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.index])
	r.index++
	return n, nil
}

func (r *chunkedReadCloser) Close() error {
	return nil
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushCount int
}

func (r *flushRecorder) Flush() {
	r.flushCount++
	r.ResponseRecorder.Flush()
}

func (s *stubProvider) Embed(_ context.Context, _ config.ProviderConfig, req provider.EmbeddingRequest, upstreamModel string) (*provider.EmbeddingResponse, error) {
	s.lastModel = upstreamModel
	s.lastEmbed = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	return s.embedResp, nil
}

func (s *stubProvider) Responses(_ context.Context, _ config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.OpenAIResponse, error) {
	s.lastModel = upstreamModel
	s.lastChat = req
	if s.returnError != nil {
		return nil, s.returnError
	}
	return s.response, nil
}

func (s *stubProvider) ResponsesStream(_ context.Context, _ config.ProviderConfig, req *provider.ChatRequest, upstreamModel string) (*provider.StreamResponse, error) {
	s.lastModel = upstreamModel
	s.lastChat = req
	return s.streamResp, nil
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
	return newHandlerWithStore(keys, rt, recorder, client, sqliteStore)
}

func newHandlerWithStore(keys []config.KeyConfig, rt *router.Router, recorder *usage.Recorder, client provider.Client, sqliteStore *store.SQLiteStore) http.Handler {
	return httpapi.NewWithClient(auth.New(keys), config.AdminConfig{Username: "admin", Password: "pass"}, client, rt, recorder, sqliteStore, []string{"openai"})
}

func TestAdminLoginPageDefaultsToEnglish(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), &stubProvider{})

	req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{`<html lang="en">`, "Sign In", "Username", "Password or API key"} {
		if !strings.Contains(body, want) {
			t.Fatalf("login page missing %q: %q", want, body)
		}
	}
}

func TestAdminLoginPageUsesChineseAcceptLanguage(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), &stubProvider{})

	req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{`<html lang="en">`, "Username", "Password or API key"} {
		if !strings.Contains(body, want) {
			t.Fatalf("English login page missing %q: %q", want, body)
		}
	}
}

func TestAdminLoginInvalidCredentialsUsesChineseAcceptLanguage(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), &stubProvider{})

	req := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=admin&password=wrong"))
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{`<html lang="en">`, "invalid credentials"} {
		if !strings.Contains(body, want) {
			t.Fatalf("English login error missing %q: %q", want, body)
		}
	}
}

func TestAdminHomeDefaultsToEnglish(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), &stubProvider{})

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=admin&password=pass"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{`<html lang="en">`, "Home", "Logout", "Providers", "Models", "Keys"} {
		if !strings.Contains(body, want) {
			t.Fatalf("English admin home expected %q: %q", want, body)
		}
	}
}

func TestAdminHomeUsageShareTranslations(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), &stubProvider{})

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=admin&password=pass"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookies[0])
	req.AddCookie(&http.Cookie{Name: "aigate_lang", Value: "zh"})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{`<html lang="zh">`, "密钥用量占比", "服务商用量占比", "模型用量占比", "按Token", "按请求数"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Chinese admin home expected %q: %q", want, body)
		}
	}
}

func TestChatCompletionsRoutesToExpectedProviderModel(t *testing.T) {
	resp := provider.OpenAIResponse{
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

func TestChatCompletionsRejectsOversizedBody(t *testing.T) {
	resp := provider.OpenAIResponse{"id": "chatcmpl-test"}
	p := &stubProvider{response: &resp}
	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "gpt-4o-mini",
		Provider:     "openai",
		UpstreamName: "gpt-4o-mini-upstream",
	}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"` + strings.Repeat("a", 33<<20) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}
	if p.lastChat != nil {
		t.Fatal("provider was called for oversized request")
	}
}

func TestChatCompletionsFailoverUsesSecondRouteWithinMaxAttempts(t *testing.T) {
	resp := provider.OpenAIResponse{"id": "chatcmpl-test"}
	p := &stubProvider{
		response:   &resp,
		chatErrors: []error{errors.New("upstream status 500")},
	}
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_first", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "first-upstream", Priority: 0},
		{ID: "mrt_second", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "second-upstream", Priority: 1},
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
		t.Fatalf("status = %d, want %d, body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if p.chatCalls != 2 {
		t.Fatalf("chatCalls = %d, want 2", p.chatCalls)
	}
	if p.lastModel != "second-upstream" {
		t.Fatalf("lastModel = %q, want second-upstream", p.lastModel)
	}
}

func TestChatCompletionsFailoverMaxAttemptsIsTotalAttempts(t *testing.T) {
	p := &stubProvider{chatErrors: []error{
		errors.New("upstream status 500"),
		errors.New("upstream status 500"),
		errors.New("upstream status 500"),
	}}
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_first", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "first-upstream", Priority: 0},
		{ID: "mrt_second", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "second-upstream", Priority: 1},
		{ID: "mrt_third", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "third-upstream", Priority: 2},
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

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
	if p.chatCalls != 2 {
		t.Fatalf("chatCalls = %d, want max total attempts 2", p.chatCalls)
	}
	if p.lastModel != "second-upstream" {
		t.Fatalf("lastModel = %q, want second-upstream", p.lastModel)
	}
}

func TestChatCompletionsDoesNotFailoverNormal4xx(t *testing.T) {
	p := &stubProvider{chatErrors: []error{errors.New("upstream status 400")}}
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_first", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "first-upstream", Priority: 0},
		{ID: "mrt_second", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "second-upstream", Priority: 1},
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

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
	if p.chatCalls != 1 {
		t.Fatalf("chatCalls = %d, want no failover for 400", p.chatCalls)
	}
}

func TestChatCompletionsProviderOverrideRoutesToProvider(t *testing.T) {
	resp := provider.OpenAIResponse{"id": "chatcmpl-test"}
	p := &stubProvider{response: &resp}
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_a", PublicName: "gpt-4o", Provider: "openai-a", UpstreamName: "gpt-4o-a", Priority: 0},
		{ID: "mrt_b", PublicName: "gpt-4o", Provider: "openai-b", UpstreamName: "gpt-4o-b", Priority: 1},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	for _, providerName := range []string{"openai-a", "openai-b"} {
		if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: providerName, BaseURL: "https://" + providerName + ".example/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: true}); err != nil {
			t.Fatalf("UpsertProvider(%s) error = %v", providerName, err)
		}
	}
	keys := []config.KeyConfig{{Key: "sk-app-001"}}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	handler := newHandlerWithStore(keys, rt, usage.New(100), p, sqliteStore)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider", "openai-b")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if p.lastModel != "gpt-4o-b" {
		t.Fatalf("lastModel = %q, want gpt-4o-b", p.lastModel)
	}
}

func TestChatCompletionsProviderOverrideDoesNotFailoverAcrossProviders(t *testing.T) {
	p := &stubProvider{
		response:   &provider.OpenAIResponse{"id": "chatcmpl-test"},
		chatErrors: []error{errors.New("upstream status 500")},
	}
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_a", PublicName: "gpt-4o", Provider: "openai-a", UpstreamName: "gpt-4o-a", Priority: 0},
		{ID: "mrt_b", PublicName: "gpt-4o", Provider: "openai-b", UpstreamName: "gpt-4o-b", Priority: 1},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	for _, providerName := range []string{"openai-a", "openai-b"} {
		if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: providerName, BaseURL: "https://" + providerName + ".example/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: true}); err != nil {
			t.Fatalf("UpsertProvider(%s) error = %v", providerName, err)
		}
	}
	keys := []config.KeyConfig{{Key: "sk-app-001"}}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	handler := newHandlerWithStore(keys, rt, usage.New(100), p, sqliteStore)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider", "openai-a")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
	if p.chatCalls != 1 {
		t.Fatalf("chatCalls = %d, want no cross-provider failover", p.chatCalls)
	}
	if p.lastModel != "gpt-4o-a" {
		t.Fatalf("lastModel = %q, want gpt-4o-a", p.lastModel)
	}
}

func TestChatCompletionsProviderOverrideStillHonorsSelectedKeyAccess(t *testing.T) {
	p := &stubProvider{response: &provider.OpenAIResponse{"id": "chatcmpl-test"}}
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_a", PublicName: "gpt-4o", Provider: "openai-a", UpstreamName: "gpt-4o-a", Priority: 0},
		{ID: "mrt_b", PublicName: "gpt-4o", Provider: "openai-b", UpstreamName: "gpt-4o-b", Priority: 1},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	for _, providerName := range []string{"openai-a", "openai-b"} {
		if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: providerName, BaseURL: "https://" + providerName + ".example/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: true}); err != nil {
			t.Fatalf("UpsertProvider(%s) error = %v", providerName, err)
		}
	}
	keys := []config.KeyConfig{{Key: "sk-app-001", ModelAccess: "selected", ModelRouteIDs: []string{"mrt_b"}}}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	handler := newHandlerWithStore(keys, rt, usage.New(100), p, sqliteStore)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Provider", "openai-a")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rr.Code, http.StatusForbidden, rr.Body.String())
	}
	if p.chatCalls != 0 {
		t.Fatalf("chatCalls = %d, want no upstream call", p.chatCalls)
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

func TestModelsReturnsOnlyVisibleDedupedRoutesForKey(t *testing.T) {
	routes := []config.ModelConfig{
		{ID: "mrt_allowed", PublicName: "gpt-4o", Provider: "openai", UpstreamName: "gpt-4o-a", Enabled: true},
		{ID: "mrt_other", PublicName: "gpt-4o", Provider: "openai", UpstreamName: "gpt-4o-b", Enabled: true},
		{ID: "mrt_disabled", PublicName: "disabled-model", Provider: "openai", UpstreamName: "disabled", Enabled: false},
		{ID: "mrt_provider_disabled", PublicName: "provider-disabled-model", Provider: "disabled-provider", UpstreamName: "x", Enabled: true},
	}
	providers := []config.ProviderConfig{
		{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "k", Enabled: true},
		{Name: "disabled-provider", BaseURL: "https://api.disabled.test/v1", APIKey: "k", Enabled: false},
	}
	rt, err := router.New(nil)
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	if err := rt.Update(routes, providers, config.RoutingConfig{Selection: "priority"}); err != nil {
		t.Fatalf("router.Update() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	for _, providerCfg := range providers {
		if err := sqliteStore.UpsertProvider(context.Background(), providerCfg); err != nil {
			t.Fatalf("UpsertProvider() error = %v", err)
		}
	}
	for _, route := range routes {
		if err := sqliteStore.UpsertModel(context.Background(), route); err != nil {
			t.Fatalf("UpsertModel() error = %v", err)
		}
	}
	keys := []config.KeyConfig{{Key: "sk-selected", ModelAccess: "selected", ModelRouteIDs: []string{"mrt_allowed"}}}
	if err := sqliteStore.UpsertAuthKey(context.Background(), keys[0]); err != nil {
		t.Fatalf("UpsertAuthKey() error = %v", err)
	}
	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-selected")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != "gpt-4o" {
		t.Fatalf("models = %#v, want only deduped gpt-4o", payload.Data)
	}
}

func TestModelDetailRequiresAuth(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/v1/models/gpt-4o-mini", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestModelDetailReturnsConfiguredModel(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/v1/models/gpt-4o-mini", nil)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if payload.ID != "gpt-4o-mini" {
		t.Fatalf("id = %q, want %q", payload.ID, "gpt-4o-mini")
	}
	if payload.Object != "model" {
		t.Fatalf("object = %q, want %q", payload.Object, "model")
	}
	if payload.OwnedBy != "aigate" {
		t.Fatalf("owned_by = %q, want %q", payload.OwnedBy, "aigate")
	}
}

func TestModelDetailReturnsNotFound(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "/v1/models/not-exists", nil)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestModelDetailSupportsSlashInModelID(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "openai/gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), &stubProvider{})
	req := httptest.NewRequest(http.MethodGet, "/v1/models/openai/gpt-4o-mini", nil)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if payload.ID != "openai/gpt-4o-mini" {
		t.Fatalf("id = %q, want %q", payload.ID, "openai/gpt-4o-mini")
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

func TestChatCompletionsStreamPassesThroughUpstreamHeaders(t *testing.T) {
	p := &stubProvider{streamResp: &provider.StreamResponse{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":      []string{"text/event-stream; charset=utf-8"},
			"Cache-Control":     []string{"no-store"},
			"X-Request-Id":      []string{"req-upstream-1"},
			"Transfer-Encoding": []string{"chunked"},
		},
		Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
	}}
	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "gpt-4o-mini",
		Provider:     "openai",
		UpstreamName: "gpt-4o-mini-upstream",
	}})
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
	if got := rr.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-type = %q, want %q", got, "text/event-stream; charset=utf-8")
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q, want %q", got, "no-store")
	}
	if got := rr.Header().Get("X-Request-Id"); got != "req-upstream-1" {
		t.Fatalf("x-request-id = %q, want %q", got, "req-upstream-1")
	}
	if got := rr.Header().Get("Transfer-Encoding"); got != "" {
		t.Fatalf("transfer-encoding = %q, want empty", got)
	}
}

func TestChatCompletionsStreamFlushesChunks(t *testing.T) {
	p := &stubProvider{streamRC: &chunkedReadCloser{chunks: []string{
		"data: {\"id\":\"chunk-1\"}\n\n",
		"data: [DONE]\n\n",
	}}}
	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "gpt-4o-mini",
		Provider:     "openai",
		UpstreamName: "gpt-4o-mini-upstream",
	}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")

	rr := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if rr.flushCount < 2 {
		t.Fatalf("flushCount = %d, want at least %d", rr.flushCount, 2)
	}
}

func TestChatCompletionsStreamLogsLifecycle(t *testing.T) {
	p := &stubProvider{streamRC: &chunkedReadCloser{chunks: []string{
		"data: first\n\n",
		"data: [DONE]\n\n",
	}}}
	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "gpt-4o-mini",
		Provider:     "openai",
		UpstreamName: "gpt-4o-mini-upstream",
	}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")

	var buf bytes.Buffer
	logger.SetOutputWithLevel(&buf, slog.LevelDebug)
	defer logger.SetOutput(os.Stdout)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	output := buf.String()
	if !strings.Contains(output, "msg=\"stream start\"") {
		t.Fatalf("missing stream_start log: %q", output)
	}
	if !strings.Contains(output, "msg=\"first chunk\"") {
		t.Fatalf("missing first_chunk log: %q", output)
	}
	if !strings.Contains(output, "msg=\"stream end\"") {
		t.Fatalf("missing stream_end log: %q", output)
	}
	if !strings.Contains(output, "saw_done=true") {
		t.Fatalf("missing saw_done=true log: %q", output)
	}
	if !strings.Contains(output, "sse_event_count=") {
		t.Fatalf("missing sse_event_count log: %q", output)
	}
}

func TestChatCompletionsStreamRecordsUsageFromFinalChunk(t *testing.T) {
	p := &stubProvider{streamRC: &chunkedReadCloser{chunks: []string{
		"data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n",
		"data: [DONE]\n\n",
	}}}
	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "gpt-4o-mini",
		Provider:     "openai",
		UpstreamName: "gpt-4o-mini-upstream",
	}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	recorder := usage.New(100)
	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, recorder, p)
	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	summary, ok := recorder.SummaryByKey("sk-app-001")
	if !ok {
		t.Fatal("SummaryByKey() ok = false, want true")
	}
	if summary.RequestTokens != 10 {
		t.Fatalf("RequestTokens = %d, want %d", summary.RequestTokens, 10)
	}
	if summary.ResponseTokens != 5 {
		t.Fatalf("ResponseTokens = %d, want %d", summary.ResponseTokens, 5)
	}
	if summary.TotalTokens != 15 {
		t.Fatalf("TotalTokens = %d, want %d", summary.TotalTokens, 15)
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

func TestChatCompletionsStreamPassesThroughUpstreamStatusAndBody(t *testing.T) {
	p := &stubProvider{streamResp: &provider.StreamResponse{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Retry-After":  []string{"30"},
		},
		Body: io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "gpt-4o-mini",
		Provider:     "openai",
		UpstreamName: "gpt-4o-mini-upstream",
	}})
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

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusTooManyRequests)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want %q", got, "application/json")
	}
	if got := rr.Header().Get("Retry-After"); got != "30" {
		t.Fatalf("retry-after = %q, want %q", got, "30")
	}
	if got := strings.TrimSpace(rr.Body.String()); got != `{"error":{"message":"rate limited"}}` {
		t.Fatalf("body = %q, want upstream error body", got)
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

func TestMessagesRoutesToExpectedProviderModel(t *testing.T) {
	resp := provider.AnthropicResponse{
		"id":   "msg-test",
		"type": "message",
	}
	p := &stubProvider{messagesResponse: &resp}
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "claude-sonnet-4-5",
			Provider:     "openai",
			UpstreamName: "claude-sonnet-4-5-upstream",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
	body := bytes.NewBufferString(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if p.lastModel != "claude-sonnet-4-5-upstream" {
		t.Fatalf("lastModel = %q, want %q", p.lastModel, "claude-sonnet-4-5-upstream")
	}
	if p.lastMessages == nil {
		t.Fatalf("lastMessages = nil")
	}
	if got, ok := p.lastMessages.Raw["messages"].([]any); !ok || len(got) != 1 {
		t.Fatalf("messages = %#v, want one message", p.lastMessages.Raw["messages"])
	}
}

func TestMessagesStreamPassesThroughUpstreamStatusAndHeaders(t *testing.T) {
	p := &stubProvider{messagesStreamResp: &provider.StreamResponse{
		StatusCode: http.StatusAccepted,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Trace-Id":   []string{"trace-123"},
		},
		Body: io.NopCloser(strings.NewReader("data: {\"type\":\"message_start\"}\n\ndata: [DONE]\n\n")),
	}}
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "claude-sonnet-4-5",
			Provider:     "openai",
			UpstreamName: "claude-sonnet-4-5-upstream",
		},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
	body := bytes.NewBufferString(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusAccepted)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want %q", got, "text/event-stream")
	}
	if got := rr.Header().Get("X-Trace-Id"); got != "trace-123" {
		t.Fatalf("x-trace-id = %q, want %q", got, "trace-123")
	}
	if !strings.Contains(rr.Body.String(), "message_start") {
		t.Fatalf("body = %q, want stream payload", rr.Body.String())
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
	resp := provider.OpenAIResponse{
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

func TestUsageQueryByModelRestAPI(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
		{PublicName: "deepseek-chat", Provider: "openai", UpstreamName: "deepseek-chat"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertUsageRollups(context.Background(), []usage.Rollup{
		{
			BucketStart:    time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-app-001"),
			KeyName:        "alice",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   1,
			SuccessCount:   1,
			RequestTokens:  10,
			ResponseTokens: 5,
			TotalTokens:    15,
		},
		{
			BucketStart:    time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-other-001"),
			KeyName:        "bob",
			Owner:          "bob",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   9,
			SuccessCount:   9,
			RequestTokens:  90,
			ResponseTokens: 45,
			TotalTokens:    135,
		},
	}); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	handler := newHandlerWithStore(
		[]config.KeyConfig{{Key: "sk-app-001", Name: "alice", Purpose: "debug"}},
		rt,
		usage.New(100),
		&stubProvider{},
		sqliteStore,
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/usage?view=by_model&model=gpt-4o-mini", nil)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		Data []struct {
			Model        string `json:"model"`
			RequestCount int64  `json:"request_count"`
			KeyCount     int64  `json:"key_count"`
			TotalTokens  int64  `json:"total_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("len(data) = %d, want %d", len(payload.Data), 1)
	}
	if payload.Data[0].Model != "gpt-4o-mini" || payload.Data[0].RequestCount != 1 || payload.Data[0].KeyCount != 1 || payload.Data[0].TotalTokens != 15 {
		t.Fatalf("unexpected usage query payload: %+v", payload.Data[0])
	}
}

func TestUsageQueryRestAPIFiltersByProvider(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertUsageRollups(context.Background(), []usage.Rollup{
		{
			BucketStart:   time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			KeyID:         usage.KeyID("sk-app-001"),
			KeyName:       "alice",
			Provider:      "openai",
			PublicModel:   "gpt-4o-mini",
			UpstreamModel: "gpt-4o-mini",
			RequestCount:  1,
			SuccessCount:  1,
			TotalTokens:   15,
		},
		{
			BucketStart:   time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC),
			KeyID:         usage.KeyID("sk-app-001"),
			KeyName:       "alice",
			Provider:      "deepseek",
			PublicModel:   "gpt-4o-mini",
			UpstreamModel: "deepseek-chat",
			RequestCount:  3,
			SuccessCount:  3,
			TotalTokens:   45,
		},
	}); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	handler := newHandlerWithStore(
		[]config.KeyConfig{{Key: "sk-app-001", Name: "alice"}},
		rt,
		usage.New(100),
		&stubProvider{},
		sqliteStore,
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/usage?view=by_model&provider=deepseek", nil)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		Data []struct {
			Model        string `json:"model"`
			RequestCount int64  `json:"request_count"`
			TotalTokens  int64  `json:"total_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].RequestCount != 3 || payload.Data[0].TotalTokens != 45 {
		t.Fatalf("unexpected provider usage payload: %+v", payload.Data)
	}
}

func TestUsageQueryByModelRestAPIEndDateIsInclusiveWithoutSpillingIntoNextDay(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = originalLocal }()

	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertUsageRollups(context.Background(), []usage.Rollup{
		{
			BucketStart:    time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-app-001"),
			KeyName:        "alice",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   1,
			SuccessCount:   1,
			RequestTokens:  10,
			ResponseTokens: 5,
			TotalTokens:    15,
		},
		{
			BucketStart:    time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-app-001"),
			KeyName:        "alice",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   2,
			SuccessCount:   2,
			RequestTokens:  20,
			ResponseTokens: 10,
			TotalTokens:    30,
		},
	}); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	handler := newHandlerWithStore(
		[]config.KeyConfig{{Key: "sk-app-001", Name: "alice", Purpose: "debug"}},
		rt,
		usage.New(100),
		&stubProvider{},
		sqliteStore,
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/usage?view=by_model&start=2026-05-10&end=2026-05-10&model=gpt-4o-mini", nil)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		Data []struct {
			Model        string `json:"model"`
			RequestCount int64  `json:"request_count"`
			TotalTokens  int64  `json:"total_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("len(data) = %d, want %d", len(payload.Data), 1)
	}
	if payload.Data[0].Model != "gpt-4o-mini" || payload.Data[0].RequestCount != 1 || payload.Data[0].TotalTokens != 15 {
		t.Fatalf("unexpected usage query payload: %+v", payload.Data[0])
	}
}

func TestUsageQueryTrendRestAPI(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertUsageRollups(context.Background(), []usage.Rollup{
		{
			BucketStart:    time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-app-001"),
			KeyName:        "alice",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   1,
			SuccessCount:   1,
			RequestTokens:  10,
			ResponseTokens: 5,
			TotalTokens:    15,
		},
	}); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	handler := newHandlerWithStore(
		[]config.KeyConfig{{Key: "sk-app-001", Name: "alice", Purpose: "debug"}},
		rt,
		usage.New(100),
		&stubProvider{},
		sqliteStore,
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/usage?view=trend&group_by=day", nil)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		Data []struct {
			Date        string `json:"date"`
			TotalTokens int64  `json:"total_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("len(data) = %d, want %d", len(payload.Data), 1)
	}
	if payload.Data[0].TotalTokens != 15 || payload.Data[0].Date == "" {
		t.Fatalf("unexpected trend payload: %+v", payload.Data[0])
	}
}

func TestUsageQueryTrendRestAPIEndDateIsInclusiveWithoutSpillingIntoNextDay(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = originalLocal }()

	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertUsageRollups(context.Background(), []usage.Rollup{
		{
			BucketStart:    time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-app-001"),
			KeyName:        "alice",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   1,
			SuccessCount:   1,
			RequestTokens:  10,
			ResponseTokens: 5,
			TotalTokens:    15,
		},
		{
			BucketStart:    time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-app-001"),
			KeyName:        "alice",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   2,
			SuccessCount:   2,
			RequestTokens:  20,
			ResponseTokens: 10,
			TotalTokens:    30,
		},
	}); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	handler := newHandlerWithStore(
		[]config.KeyConfig{{Key: "sk-app-001", Name: "alice", Purpose: "debug"}},
		rt,
		usage.New(100),
		&stubProvider{},
		sqliteStore,
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/usage?view=trend&start=2026-05-10&end=2026-05-10&group_by=day", nil)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var payload struct {
		Data []struct {
			Date         string `json:"date"`
			RequestCount int64  `json:"request_count"`
			TotalTokens  int64  `json:"total_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("len(data) = %d, want %d", len(payload.Data), 1)
	}
	if payload.Data[0].Date != "2026-05-10" || payload.Data[0].RequestCount != 1 || payload.Data[0].TotalTokens != 15 {
		t.Fatalf("unexpected trend payload: %+v", payload.Data[0])
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

func TestAdminHomeRendersDashboardWithRealData(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = originalLocal }()

	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_openai", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini", Enabled: true},
		{ID: "mrt_deepseek", PublicName: "deepseek-chat", Provider: "deepseek", UpstreamName: "deepseek-chat", Enabled: true},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: "openai", BaseURL: "https://openai.example/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: true}); err != nil {
		t.Fatalf("UpsertProvider(openai) error = %v", err)
	}
	if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: "deepseek", BaseURL: "https://deepseek.example/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: false}); err != nil {
		t.Fatalf("UpsertProvider(deepseek) error = %v", err)
	}
	if err := sqliteStore.UpsertModel(context.Background(), config.ModelConfig{ID: "mrt_openai", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini", Enabled: true}); err != nil {
		t.Fatalf("UpsertModel(openai) error = %v", err)
	}
	if err := sqliteStore.UpsertModel(context.Background(), config.ModelConfig{ID: "mrt_deepseek", PublicName: "deepseek-chat", Provider: "deepseek", UpstreamName: "deepseek-chat", Enabled: false}); err != nil {
		t.Fatalf("UpsertModel(deepseek) error = %v", err)
	}
	keys := []config.KeyConfig{
		{Key: "sk-alice-001", Name: "alice-key", Owner: "alice"},
		{Key: "sk-bob-001", Name: "bob-key", Owner: "bob"},
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	today := time.Now().In(time.Local)
	todayStart := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	if err := sqliteStore.UpsertUsageRollups(context.Background(), []usage.Rollup{
		{
			BucketStart:   todayStart.Add(2 * time.Hour),
			KeyID:         usage.KeyID("sk-alice-001"),
			KeyName:       "alice-key",
			Owner:         "alice",
			Provider:      "openai",
			PublicModel:   "gpt-4o-mini",
			UpstreamModel: "gpt-4o-mini",
			RequestCount:  12,
			SuccessCount:  11,
			ErrorCount:    1,
			TotalTokens:   12500,
		},
	}); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=admin&password=pass"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{"Home", "Today", "12.5K", "Users / Keys", "Top Keys", "Top Providers", "Top Models", "By Token", "By Requests", "\"name\":\"alice-key\"", "\"name\":\"openai\"", "\"name\":\"gpt-4o-mini\"", "chartId:'homeKeyPie'", "chartId:'homeProviderPie'", "chartId:'homeModelPie'", "pie-metric-btn"} {
		if !strings.Contains(body, want) {
			t.Fatalf("home page missing %q: %q", want, body)
		}
	}
	if strings.Contains(body, "k+'Pie'") {
		t.Fatalf("home page should not derive pie chart IDs from metric keys: %q", body)
	}
	if strings.Contains(body, "Public Models") {
		t.Fatalf("home page should not use public models label: %q", body)
	}
}

func TestAdminUsageViewRendersSinglePageWithEmbeddedData(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = originalLocal }()

	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
		{PublicName: "deepseek-chat", Provider: "openai", UpstreamName: "deepseek-chat"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	keys := []config.KeyConfig{
		{Key: "sk-alice-001", Name: "alice-key", Owner: "alice", Purpose: "debug"},
		{Key: "sk-bob-001", Name: "bob-key", Owner: "bob", Purpose: "prod"},
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	if err := sqliteStore.UpsertUsageRollups(context.Background(), []usage.Rollup{
		{
			BucketStart:    time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-alice-001"),
			KeyName:        "alice-key",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   2,
			SuccessCount:   2,
			RequestTokens:  20,
			ResponseTokens: 10,
			TotalTokens:    30,
		},
		{
			BucketStart:    time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-alice-001"),
			KeyName:        "alice-key",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "deepseek-chat",
			UpstreamModel:  "deepseek-chat",
			RequestCount:   1,
			SuccessCount:   1,
			RequestTokens:  10,
			ResponseTokens: 5,
			TotalTokens:    15,
		},
		{
			BucketStart:    time.Date(2026, 5, 10, 2, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-bob-001"),
			KeyName:        "bob-key",
			Owner:          "bob",
			Purpose:        "prod",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   4,
			SuccessCount:   3,
			ErrorCount:     1,
			RequestTokens:  40,
			ResponseTokens: 20,
			TotalTokens:    60,
		},
	}); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)

	loginBody := bytes.NewBufferString("username=admin&password=pass")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	values := url.Values{
		"start": {"2026-05-10"},
		"end":   {"2026-05-10"},
		"model": {"gpt-4o-mini"},
		"key":   {usage.KeyID("sk-alice-001")},
		"owner": {"alice"},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/usage/view?"+values.Encode(), nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, ">Trend <span") || !strings.Contains(body, ">By Model <span") || !strings.Contains(body, ">By Key <span") {
		t.Fatalf("usage page missing sections: %q", body)
	}
	if trendIdx, modelIdx, keyIdx := strings.Index(body, ">Trend <span"), strings.Index(body, ">By Model <span"), strings.Index(body, ">By Key <span"); !(trendIdx >= 0 && modelIdx > trendIdx && keyIdx > modelIdx) {
		t.Fatalf("unexpected section order: trend=%d model=%d key=%d", trendIdx, modelIdx, keyIdx)
	}
	if !strings.Contains(body, "data-section-target=\"trendSectionBody\"") || !strings.Contains(body, "data-section-target=\"modelSectionBody\"") || !strings.Contains(body, "data-section-target=\"keySectionBody\"") {
		t.Fatalf("usage page missing collapsible section toggles: %q", body)
	}
	if !strings.Contains(body, "Chart by:") || !strings.Contains(body, "By Token") || !strings.Contains(body, "By Requests") {
		t.Fatalf("usage page missing clear pie metric controls: %q", body)
	}
	if !strings.Contains(body, "name=\"key\"") || !strings.Contains(body, "name=\"owner\"") {
		t.Fatalf("usage page missing key/owner filters: %q", body)
	}
	for _, want := range []string{"setQuickDate(1)", "setQuickDate(7)", "setQuickDate(30)"} {
		if !strings.Contains(body, want) {
			t.Fatalf("usage page missing quick date %q: %q", want, body)
		}
	}
	if !strings.Contains(body, "<option value=\""+usage.KeyID("sk-alice-001")+"\" selected") {
		t.Fatalf("usage page missing selected key filter: %q", body)
	}
	if !strings.Contains(body, "<option value=\"alice\" selected") {
		t.Fatalf("usage page missing selected owner filter: %q", body)
	}
	if strings.Contains(body, "fetch(") || strings.Contains(body, "/admin/usage/trend") {
		t.Fatalf("usage page still depends on trend fetch: %q", body)
	}
	if !strings.Contains(body, "id=\"trendHourData\"") || !strings.Contains(body, "id=\"trendDayData\"") {
		t.Fatalf("usage page missing embedded trend data: %q", body)
	}
	if !strings.Contains(body, "aCell?.dataset.tokens") || !strings.Contains(body, "bCell?.dataset.tokens") {
		t.Fatalf("usage page missing token-cell numeric sort fallback: %q", body)
	}
	if !strings.Contains(body, "const nextAsc = isSameCol ? table.dataset.sortAsc !== \"true\" : kind !== \"num\";") {
		t.Fatalf("usage page missing sort toggle logic: %q", body)
	}
	if !strings.Contains(body, "\"date\":\"2026-05-10 00:00\"") || !strings.Contains(body, "\"date\":\"2026-05-10\"") {
		t.Fatalf("usage page missing expected trend points: %q", body)
	}
	if !strings.Contains(body, "\"name\":\"gpt-4o-mini\"") {
		t.Fatalf("usage page missing model pie data: %q", body)
	}
	if strings.Contains(body, "\"name\":\"deepseek-chat\"") {
		t.Fatalf("usage page unexpectedly includes filtered-out model data: %q", body)
	}
	if !strings.Contains(body, "\"name\":\"alice-key\"") {
		t.Fatalf("usage page missing key pie data: %q", body)
	}
}

func TestAdminUsageViewFiltersByProvider(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = originalLocal }()

	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	keys := []config.KeyConfig{{Key: "sk-alice-001", Name: "alice-key", Owner: "alice"}}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	if err := sqliteStore.UpsertUsageRollups(context.Background(), []usage.Rollup{
		{
			BucketStart:   time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			KeyID:         usage.KeyID("sk-alice-001"),
			KeyName:       "alice-key",
			Owner:         "alice",
			Provider:      "openai",
			PublicModel:   "gpt-4o-mini",
			UpstreamModel: "gpt-4o-mini",
			RequestCount:  1,
			SuccessCount:  1,
			TotalTokens:   15,
		},
		{
			BucketStart:   time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC),
			KeyID:         usage.KeyID("sk-alice-001"),
			KeyName:       "alice-key",
			Owner:         "alice",
			Provider:      "deepseek",
			PublicModel:   "deepseek-chat",
			UpstreamModel: "deepseek-chat",
			RequestCount:  2,
			SuccessCount:  2,
			TotalTokens:   30,
		},
	}); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=admin&password=pass"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/view?start=2026-05-10&end=2026-05-10&provider=deepseek", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "<option value=\"deepseek\" selected") {
		t.Fatalf("usage page missing selected provider: %q", body)
	}
	if !strings.Contains(body, "\"name\":\"deepseek-chat\"") {
		t.Fatalf("usage page missing provider filtered model: %q", body)
	}
	if strings.Contains(body, "\"name\":\"gpt-4o-mini\"") {
		t.Fatalf("usage page includes unselected provider model: %q", body)
	}
}

func TestAdminUsageViewHidesOwnerFilterForUserAndIgnoresOwnerQuery(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = originalLocal }()

	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	keys := []config.KeyConfig{
		{Key: "sk-alice-001", Name: "alice-key", Owner: "alice", Purpose: "debug"},
		{Key: "sk-bob-001", Name: "bob-key", Owner: "bob", Purpose: "prod"},
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	if err := sqliteStore.UpsertUsageRollups(context.Background(), []usage.Rollup{
		{
			BucketStart:    time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-alice-001"),
			KeyName:        "alice-key",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   2,
			SuccessCount:   2,
			RequestTokens:  20,
			ResponseTokens: 10,
			TotalTokens:    30,
		},
		{
			BucketStart:    time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-bob-001"),
			KeyName:        "bob-key",
			Owner:          "bob",
			Purpose:        "prod",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   4,
			SuccessCount:   3,
			ErrorCount:     1,
			RequestTokens:  40,
			ResponseTokens: 20,
			TotalTokens:    60,
		},
	}); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)

	loginBody := bytes.NewBufferString("username=alice&password=sk-alice-001")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected user session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/view?start=2026-05-10&end=2026-05-10&owner=bob", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if strings.Contains(body, "name=\"owner\"") {
		t.Fatalf("owner filter should be hidden for user: %q", body)
	}
	if !strings.Contains(body, "\"name\":\"alice-key\"") {
		t.Fatalf("user page missing own usage data: %q", body)
	}
	if strings.Contains(body, "\"name\":\"bob-key\"") {
		t.Fatalf("user page unexpectedly includes other owner data: %q", body)
	}
}

func TestAdminUsageViewShowsEmptyStatesForNoData(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	defer func() { time.Local = originalLocal }()

	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	keys := []config.KeyConfig{{Key: "sk-alice-001", Name: "alice-key", Owner: "alice", Purpose: "debug"}}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}

	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)

	loginBody := bytes.NewBufferString("username=admin&password=pass")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/view?start=2026-05-10&end=2026-05-10&key="+url.QueryEscape(usage.KeyID("sk-alice-001")), nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	hasSectionEmptyState := func(sectionID string) bool {
		idx := strings.Index(body, "id=\""+sectionID+"\"")
		if idx < 0 {
			return false
		}
		snippet := body[idx:]
		if len(snippet) > 240 {
			snippet = snippet[:240]
		}
		return strings.Contains(snippet, "<div class=\"no-data\">No usage data for the selected filters</div>")
	}
	if !strings.Contains(body, "<div id=\"trendChart\"><div class=\"no-data\">No usage data for the selected filters</div></div>") {
		t.Fatalf("trend empty state missing: %q", body)
	}
	if !hasSectionEmptyState("modelSectionBody") {
		t.Fatalf("model empty state missing: %q", body)
	}
	if !hasSectionEmptyState("keySectionBody") {
		t.Fatalf("key empty state missing: %q", body)
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
	resp := provider.OpenAIResponse{"id": "chatcmpl-test"}
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
	if got, ok := p.lastChat.Raw["reasoning_effort"].(string); !ok || got != "high" {
		t.Fatalf("reasoning_effort = %#v, want %q", p.lastChat.Raw["reasoning_effort"], "high")
	}
	thinking, ok := p.lastChat.Raw["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %#v, want map", p.lastChat.Raw["thinking"])
	}
	if got, ok := thinking["type"].(string); !ok || got != "enabled" {
		t.Fatalf("thinking.type = %#v, want %q", thinking["type"], "enabled")
	}
}

func TestChatCompletionsPreservesAgentMultiTurnMessagesAndToolCalls(t *testing.T) {
	resp := provider.OpenAIResponse{"id": "chatcmpl-test"}
	p := &stubProvider{response: &resp}
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
	body := bytes.NewBufferString(`{
		"model":"gpt-4o-mini",
		"messages":[
			{"role":"user","content":"先查北京天气"},
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"beijing\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"晴 26C"},
			{"role":"user","content":"那上海呢"}
		],
		"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object"}}}],
		"parallel_tool_calls":true
	}`)
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
	rawMessages, ok := p.lastChat.Raw["messages"].([]any)
	if !ok {
		t.Fatalf("raw messages = %#v, want []any", p.lastChat.Raw["messages"])
	}
	if len(rawMessages) != 4 {
		t.Fatalf("len(raw messages) = %d, want %d", len(rawMessages), 4)
	}
	second, ok := rawMessages[1].(map[string]any)
	if !ok {
		t.Fatalf("messages[1] = %#v, want map", rawMessages[1])
	}
	toolCalls, ok := second["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("messages[1].tool_calls = %#v, want one call", second["tool_calls"])
	}
	third, ok := rawMessages[2].(map[string]any)
	if !ok {
		t.Fatalf("messages[2] = %#v, want map", rawMessages[2])
	}
	if got, ok := third["tool_call_id"].(string); !ok || got != "call_1" {
		t.Fatalf("messages[2].tool_call_id = %#v, want %q", third["tool_call_id"], "call_1")
	}
	if got, ok := p.lastChat.Raw["parallel_tool_calls"].(bool); !ok || !got {
		t.Fatalf("parallel_tool_calls = %#v, want true", p.lastChat.Raw["parallel_tool_calls"])
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

func TestAdminKeysFormUserCannotOverwriteAnotherUsersKey(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_allowed", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini", Enabled: true},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	keys := []config.KeyConfig{
		{Key: "sk-alice-001", Name: "alice-key", Owner: "alice", ModelAccess: "selected", ModelRouteIDs: []string{"mrt_allowed"}},
		{Key: "sk-bob-001", Name: "bob-key", Owner: "bob"},
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=alice&password=sk-alice-001"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected user session cookie")
	}

	form := url.Values{}
	form.Set("key", "sk-bob-001")
	form.Set("name", "taken")
	form.Set("model_access", "selected")
	form.Add("model_route_ids", "mrt_allowed")
	req := httptest.NewRequest(http.MethodPost, "/admin/keys", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
	bob, err := sqliteStore.GetAuthKey(context.Background(), "sk-bob-001")
	if err != nil {
		t.Fatalf("GetAuthKey(bob) error = %v", err)
	}
	if bob.Owner != "bob" || bob.Name != "bob-key" {
		t.Fatalf("bob key was overwritten: %+v", bob)
	}
}

func TestAdminKeysFormUserCannotDeleteLastOwnKey(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_allowed", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini", Enabled: true},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	keys := []config.KeyConfig{
		{Key: "sk-alice-001", Name: "alice-key", Owner: "alice", ModelAccess: "selected", ModelRouteIDs: []string{"mrt_allowed"}},
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=alice&password=sk-alice-001"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected user session cookie")
	}

	form := url.Values{}
	form.Set("key", "sk-alice-001")
	req := httptest.NewRequest(http.MethodPost, "/admin/keys/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
	if _, err := sqliteStore.GetAuthKey(context.Background(), "sk-alice-001"); err != nil {
		t.Fatalf("last key was deleted: %v", err)
	}
}

func TestAdminKeysPageMasksKeyByDefault(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{
		{Key: "sk-bootstrap-001"},
		{Key: "sk-user-001", Name: "user1", ModelAccess: "selected", ModelRouteIDs: []string{"mrt_a", "mrt_b"}},
	}, rt, usage.New(100), &stubProvider{})

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
	if !strings.Contains(body, ">Copy<") {
		t.Fatalf("body missing copy button: %q", body)
	}
	if !strings.Contains(body, "onclick=\"toggleKeyText(this)\"") {
		t.Fatalf("body missing key click toggle handler: %q", body)
	}
	if !strings.Contains(body, "onclick=\"generateKey()\"") {
		t.Fatalf("body missing generate key button: %q", body)
	}
	if !strings.Contains(body, "sk-") {
		t.Fatalf("body missing sk- key prefix in generator: %q", body)
	}
	if !strings.Contains(body, "submitKeyDelete") {
		t.Fatalf("body missing key delete handler: %q", body)
	}
	if !strings.Contains(body, "Limited · 2 routes") {
		t.Fatalf("body missing limited route count: %q", body)
	}
}

func TestAdminKeysPageChineseModelAccessCopy(t *testing.T) {
	models := []config.ModelConfig{
		{ID: "mrt_a", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini", Enabled: true},
		{ID: "mrt_b", PublicName: "deepseek-chat", Provider: "deepseek", UpstreamName: "deepseek-chat", Enabled: true},
	}
	rt, err := router.New([]config.ModelConfig{
		models[0],
		models[1],
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	for _, model := range models {
		if err := sqliteStore.UpsertModel(context.Background(), model); err != nil {
			t.Fatalf("UpsertModel() error = %v", err)
		}
	}
	keys := []config.KeyConfig{
		{Key: "sk-bootstrap-001"},
		{Key: "sk-user-001", Name: "user1", ModelAccess: "selected", ModelRouteIDs: []string{"mrt_a", "mrt_b"}},
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)

	loginBody := bytes.NewBufferString("username=admin&password=pass")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/admin/keys", nil)
	pageReq.AddCookie(cookies[0])
	pageReq.AddCookie(&http.Cookie{Name: "aigate_lang", Value: "zh"})
	pageRR := httptest.NewRecorder()
	handler.ServeHTTP(pageRR, pageReq)

	if pageRR.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", pageRR.Code, http.StatusOK)
	}
	body := pageRR.Body.String()
	for _, want := range []string{
		"创建 API Key，可选填写用户和用途。",
		"更新此 API Key 的名称、用户或用途。",
		"<option value=\"selected\">选择</option>",
		"指定模型 · 2 条路由",
		"<span class=\"route-selected-count\">0 已选</span>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Chinese keys page missing %q: %q", want, body)
		}
	}
	if strings.Contains(body, "已选 · 2 条路由") {
		t.Fatalf("Chinese keys page should not use selected state copy for limited route badge: %q", body)
	}
}

func TestAdminKeysPageRouteSelectorHidesRouteIDText(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_key_route", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini", Enabled: true},
		{ID: "mrt_key_route_azure", PublicName: "gpt-4o-mini", Provider: "azure-us", UpstreamName: "gpt-4o-2024-11", Enabled: true},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertModel(context.Background(), config.ModelConfig{ID: "mrt_key_route", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini", Enabled: true}); err != nil {
		t.Fatalf("UpsertModel() error = %v", err)
	}
	if err := sqliteStore.UpsertModel(context.Background(), config.ModelConfig{ID: "mrt_key_route_azure", PublicName: "gpt-4o-mini", Provider: "azure-us", UpstreamName: "gpt-4o-2024-11", Enabled: true}); err != nil {
		t.Fatalf("UpsertModel() error = %v", err)
	}
	keys := []config.KeyConfig{{Key: "sk-bootstrap-001", Name: "bootstrap"}}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=admin&password=pass"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/keys", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<span class=\"mono\">mrt_key_route</span>") {
		t.Fatalf("keys page visibly includes route id: %q", body)
	}
	if strings.Contains(body, "gpt-4o-mini / openai / gpt-4o-mini") {
		t.Fatalf("keys page still uses flat route label: %q", body)
	}
	if !strings.Contains(body, "<span>gpt-4o-mini</span><span class=\"route-selected-count\">0 selected</span>") {
		t.Fatalf("keys page missing public model group title: %q", body)
	}
	if !strings.Contains(body, "Disabled providers/routes are never usable.") {
		t.Fatalf("keys page missing model access rule hint: %q", body)
	}
	if !strings.Contains(body, ".route-access-block[hidden]{display:none}") {
		t.Fatalf("keys page missing route access hidden style: %q", body)
	}
	if !strings.Contains(body, "if(access !== 'selected'){return [];}") {
		t.Fatalf("keys page should not submit route ids when model access is all: %q", body)
	}
	if !strings.Contains(body, "<span class=\"route-provider\">openai</span>") {
		t.Fatalf("keys page missing provider route label: %q", body)
	}
	if !strings.Contains(body, "upstream: gpt-4o-2024-11") {
		t.Fatalf("keys page missing upstream detail for renamed route: %q", body)
	}
	if !strings.Contains(body, "value=\"mrt_key_route\"") {
		t.Fatalf("keys page missing submitted route id value: %q", body)
	}
}

func TestUserModelsAndPlaygroundOnlyShowAccessibleRoutes(t *testing.T) {
	models := []config.ModelConfig{
		{ID: "mrt_allowed", PublicName: "allowed-model", Provider: "openai-a", UpstreamName: "allowed-upstream", Enabled: true},
		{ID: "mrt_denied", PublicName: "denied-model", Provider: "openai-b", UpstreamName: "denied-upstream", Enabled: true},
	}
	rt, err := router.New(models)
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: "openai-a", BaseURL: "https://a.example/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: true}); err != nil {
		t.Fatalf("UpsertProvider(openai-a) error = %v", err)
	}
	if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: "openai-b", BaseURL: "https://b.example/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: true}); err != nil {
		t.Fatalf("UpsertProvider(openai-b) error = %v", err)
	}
	for _, model := range models {
		if err := sqliteStore.UpsertModel(context.Background(), model); err != nil {
			t.Fatalf("UpsertModel() error = %v", err)
		}
	}
	keys := []config.KeyConfig{{Key: "sk-alice-001", Name: "alice-key", Owner: "alice", ModelAccess: "selected", ModelRouteIDs: []string{"mrt_allowed"}}}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=alice&password=sk-alice-001"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected user session cookie")
	}

	for _, path := range []string{"/admin/models", "/admin/playground"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookies[0])
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", path, rr.Code, http.StatusOK)
		}
		body := rr.Body.String()
		for _, want := range []string{"allowed-model", "openai-a"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing accessible %q: %q", path, want, body)
			}
		}
		for _, forbidden := range []string{"denied-model", "openai-b", "denied-upstream"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s leaked inaccessible %q: %q", path, forbidden, body)
			}
		}
	}
}

func TestAdminProvidersPageHasDeleteConfirm(t *testing.T) {
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
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/providers", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "submitProviderDelete") {
		t.Fatalf("providers page missing delete confirm: %q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Connection") {
		t.Fatalf("providers page missing connection column: %q", rr.Body.String())
	}
}

func TestAdminProvidersTableHidesConnectionDetails(t *testing.T) {
	rt, err := router.New(nil)
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{
		Name:             "private-provider",
		BaseURL:          "https://private.example/v1",
		AnthropicBaseURL: "https://anthropic.private.example",
		AnthropicVersion: "2023-06-01",
		APIKeyRef:        "PRIVATE_SECRET_REF",
		TimeoutSeconds:   30,
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertProvider() error = %v", err)
	}
	handler := httpapi.NewWithClient(auth.New(nil), config.AdminConfig{Username: "admin", Password: "pass"}, &stubProvider{}, rt, usage.New(100), sqliteStore, []string{"private-provider"})

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=admin&password=pass"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/providers", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	tableStart := strings.Index(body, "<table id=\"providers-table\"")
	tableEnd := strings.Index(body, "</table>")
	if tableStart < 0 || tableEnd < tableStart {
		t.Fatalf("providers table missing: %q", body)
	}
	tableHTML := body[tableStart:tableEnd]
	for _, hidden := range []string{"https://private.example/v1", "https://anthropic.private.example", "2023-06-01", "PRIVATE_SECRET_REF"} {
		if strings.Contains(tableHTML, hidden) {
			t.Fatalf("providers table leaked %q: %q", hidden, tableHTML)
		}
	}
	for _, want := range []string{"OpenAI", "Anthropic", "Key env"} {
		if !strings.Contains(tableHTML, want) {
			t.Fatalf("providers table missing %q: %q", want, tableHTML)
		}
	}
	if strings.Contains(tableHTML, "Credentials configured") {
		t.Fatalf("providers table still uses generic credential label: %q", tableHTML)
	}
}

func TestAdminProviderSaveRejectsInvalidBaseURL(t *testing.T) {
	rt, err := router.New(nil)
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	handler := httpapi.NewWithClient(auth.New(nil), config.AdminConfig{Username: "admin", Password: "pass"}, &stubProvider{}, rt, usage.New(100), sqliteStore, nil)

	loginBody := bytes.NewBufferString("username=admin&password=pass")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	form := url.Values{
		"name":     {"bad-provider"},
		"base_url": {"api.example.test/v1"},
		"api_key":  {"secret"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/providers", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if _, err := sqliteStore.GetProvider(context.Background(), "bad-provider"); err == nil {
		t.Fatal("invalid provider was persisted")
	}
}

func TestAdminModelsPageHasDeleteConfirm(t *testing.T) {
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
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/models", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "submitModelDelete") {
		t.Fatalf("models page missing delete handler: %q", rr.Body.String())
	}
	for _, want := range []string{"name=\"create_mode\"", "value=\"batch\"", "name=\"batch_provider\"", "name=\"batch_models\"", "parseBatchModelLines", "submitModelBatchCreate", "batch-model-preview", "priority: lower number first", "weight/random: sticky by session/body", "Lower number wins.", "Used only when selection is weight."} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("models page missing batch create support %q: %q", want, rr.Body.String())
		}
	}
}

func TestAdminModelsPageHidesRouteIDColumn(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_visible_route", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini", Enabled: true},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertModel(context.Background(), config.ModelConfig{ID: "mrt_visible_route", PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini", Enabled: true}); err != nil {
		t.Fatalf("UpsertModel() error = %v", err)
	}
	handler := newHandlerWithStore([]config.KeyConfig{{Key: "sk-bootstrap-001"}}, rt, usage.New(100), &stubProvider{}, sqliteStore)

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=admin&password=pass"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/models", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if strings.Contains(body, "Route ID") || strings.Contains(body, "<td class=\"mono\">mrt_visible_route</td>") {
		t.Fatalf("models page visibly includes route id: %q", body)
	}
	if !strings.Contains(body, "data-route-id=\"mrt_visible_route\"") {
		t.Fatalf("models page missing hidden route id action data: %q", body)
	}
}

func TestAdminPlaygroundRequiresSession(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), &stubProvider{})
	req := httptest.NewRequest(http.MethodGet, "/admin/playground", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
}

func TestAdminPlaygroundInitializesChatWindow(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "openai/gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001", Name: "alice"}}, rt, usage.New(100), &stubProvider{})

	loginBody := bytes.NewBufferString("username=admin&password=pass")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/playground", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "toggleMain();") || !strings.Contains(body, "updateConnectionValues();") || !strings.Contains(body, "updateModeUI();") {
		t.Fatalf("playground body missing init calls: %q", body)
	}
	if !strings.Contains(body, "overflow-wrap:anywhere") || !strings.Contains(body, ".grid>*{min-width:0}") {
		t.Fatalf("playground body missing long-content overflow guards: %q", body)
	}
	if strings.Contains(body, "document.getElementById('connectKv').style.display = '';") {
		t.Fatalf("playground body still contains stale inline connectKv display script: %q", body)
	}
}

func TestAdminPlaygroundUsesPublicAPIsDirectly(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{ID: "mrt_a", PublicName: "gpt-4o", Provider: "openai-a", UpstreamName: "gpt-4o-a", Enabled: true},
		{ID: "mrt_b", PublicName: "gpt-4o", Provider: "openai-b", UpstreamName: "gpt-4o-b", Enabled: true},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: "openai-a", BaseURL: "https://a.example/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: true}); err != nil {
		t.Fatalf("UpsertProvider(openai-a) error = %v", err)
	}
	if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: "openai-b", BaseURL: "https://b.example/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: true}); err != nil {
		t.Fatalf("UpsertProvider(openai-b) error = %v", err)
	}
	keys := []config.KeyConfig{{Key: "sk-app-001", Name: "alice"}}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	handler := newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore)

	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", bytes.NewBufferString("username=admin&password=pass"))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected admin session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/playground", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{"/v1/chat/completions", "/v1/responses", "/anthropic/v1/messages", "Authorization", "X-Provider", "providerOptions", "Default route", "openai-a", "openai-b", "Request Preview", "Copy curl", "curl -X POST", "curlCommand(false)", "Adds X-Provider; failover stays within provider."} {
		if !strings.Contains(body, want) {
			t.Fatalf("playground body missing %q: %q", want, body)
		}
	}
	if strings.Contains(body, "stopBtn") || strings.Contains(body, "stopStream") || strings.Contains(body, "Stop streaming") {
		t.Fatalf("playground still exposes stop controls: %q", body)
	}
	if strings.Contains(body, "/admin/playground/chat") || strings.Contains(body, "/admin/playground/chat/ajax") {
		t.Fatalf("playground still references admin chat proxy: %q", body)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/admin/playground/chat", strings.NewReader("api_key=sk-app-001"))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(cookies[0])
	postRR := httptest.NewRecorder()
	handler.ServeHTTP(postRR, postReq)
	if postRR.Code == http.StatusOK {
		t.Fatalf("old playground chat route still handled successfully")
	}
}

func TestUserLoginShowsOnlyOwnerKeys(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "openai/gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	keys := []config.KeyConfig{
		{Key: "sk-alice-001", Name: "alice-key", Owner: "alice"},
		{Key: "sk-bob-001", Name: "bob-key", Owner: "bob"},
	}
	handler := newHandler(t, keys, rt, usage.New(100), &stubProvider{})

	loginBody := bytes.NewBufferString("username=alice&password=sk-alice-001")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	if loginRR.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want %d", loginRR.Code, http.StatusSeeOther)
	}
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected user session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/keys", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "alice-key") {
		t.Fatalf("missing alice key row: %q", body)
	}
	if strings.Contains(body, "bob-key") {
		t.Fatalf("unexpected bob key row: %q", body)
	}
	if strings.Contains(body, "placeholder=\"owner\"") {
		t.Fatalf("owner input should be hidden for non-admin: %q", body)
	}
}

func TestUserCannotAccessProvidersPage(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "openai/gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	keys := []config.KeyConfig{{Key: "sk-alice-001", Name: "alice-key", Owner: "alice"}}
	handler := newHandler(t, keys, rt, usage.New(100), &stubProvider{})

	loginBody := bytes.NewBufferString("username=alice&password=sk-alice-001")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected user session cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/providers", nil)
	req.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestAdminUsageFiltersByOwnerForUserSession(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	recorder := usage.New(100)
	recorder.Record(usage.NewRecord(auth.Principal{Key: "sk-alice-001", Name: "alice-key", Owner: "alice"}, "chat.completions", "openai", "gpt-4o-mini", "gpt-4o-mini", true, 1, 2, 3, http.StatusOK, 0))
	recorder.Record(usage.NewRecord(auth.Principal{Key: "sk-bob-001", Name: "bob-key", Owner: "bob"}, "chat.completions", "openai", "gpt-4o-mini", "gpt-4o-mini", true, 1, 2, 3, http.StatusOK, 0))
	keys := []config.KeyConfig{
		{Key: "sk-alice-001", Name: "alice-key", Owner: "alice"},
		{Key: "sk-bob-001", Name: "bob-key", Owner: "bob"},
	}
	handler := newHandler(t, keys, rt, recorder, &stubProvider{})

	loginBody := bytes.NewBufferString("username=alice&password=sk-alice-001")
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/login", loginBody)
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRR := httptest.NewRecorder()
	handler.ServeHTTP(loginRR, loginReq)
	cookies := loginRR.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected user session cookie")
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
	if len(payload.Data) != 1 {
		t.Fatalf("len(data) = %d, want %d", len(payload.Data), 1)
	}
	if got, _ := payload.Data[0]["owner"].(string); got != "alice" {
		t.Fatalf("owner = %q, want %q", got, "alice")
	}
}

func TestMessagesNonStreamRecordsUsage(t *testing.T) {
	resp := provider.AnthropicResponse{
		"id":   "msg-test",
		"type": "message",
		"usage": map[string]any{
			"input_tokens":  11,
			"output_tokens": 257,
		},
	}
	p := &stubProvider{messagesResponse: &resp}
	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "claude-sonnet-4-5",
		Provider:     "openai",
		UpstreamName: "claude-sonnet-4-5-upstream",
	}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	recorder := usage.New(100)
	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, recorder, p)
	body := bytes.NewBufferString(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	summary, ok := recorder.SummaryByKey("sk-app-001")
	if !ok {
		t.Fatal("SummaryByKey() ok = false, want true")
	}
	if summary.RequestTokens != 11 {
		t.Fatalf("RequestTokens = %d, want %d", summary.RequestTokens, 11)
	}
	if summary.ResponseTokens != 257 {
		t.Fatalf("ResponseTokens = %d, want %d", summary.ResponseTokens, 257)
	}
	if summary.TotalTokens != 268 {
		t.Fatalf("TotalTokens = %d, want %d", summary.TotalTokens, 268)
	}
}

func TestMessagesStreamRecordsUsageFromMessageDelta(t *testing.T) {
	p := &stubProvider{messagesStreamResp: &provider.StreamResponse{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &chunkedReadCloser{chunks: []string{
			"event:message_start\ndata:{\"message\":{\"model\":\"qwen3.6-plus\",\"id\":\"msg_xxx\",\"role\":\"assistant\",\"type\":\"message\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}},\"type\":\"message_start\"}\n\n",
			"event:content_block_start\ndata:{\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\"},\"index\":0}\n\n",
			"event:content_block_delta\ndata:{\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"},\"type\":\"content_block_delta\",\"index\":0}\n\n",
			"event:content_block_stop\ndata:{\"type\":\"content_block_stop\",\"index\":0}\n\n",
			"event:message_delta\ndata:{\"delta\":{\"stop_reason\":\"end_turn\"},\"type\":\"message_delta\",\"usage\":{\"input_tokens\":11,\"output_tokens\":213}}\n\n",
			"event:message_stop\ndata:{\"type\":\"message_stop\"}\n\n",
		}},
	}}
	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "claude-sonnet-4-5",
		Provider:     "openai",
		UpstreamName: "claude-sonnet-4-5-upstream",
	}})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	recorder := usage.New(100)
	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, recorder, p)
	body := bytes.NewBufferString(`{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", body)
	req.Header.Set("Authorization", "Bearer sk-app-001")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	summary, ok := recorder.SummaryByKey("sk-app-001")
	if !ok {
		t.Fatal("SummaryByKey() ok = false, want true")
	}
	if summary.RequestTokens != 11 {
		t.Fatalf("RequestTokens = %d, want %d", summary.RequestTokens, 11)
	}
	if summary.ResponseTokens != 213 {
		t.Fatalf("ResponseTokens = %d, want %d", summary.ResponseTokens, 213)
	}
	if summary.TotalTokens != 224 {
		t.Fatalf("TotalTokens = %d, want %d", summary.TotalTokens, 224)
	}
}

func TestResponsesRoutesToExpectedProviderModel(t *testing.T) {
	resp := provider.OpenAIResponse{
		"id":     "resp-test",
		"object": "response",
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
	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
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
	if p.lastChat == nil {
		t.Fatalf("lastChat = nil")
	}
	if input, ok := p.lastChat.Raw["input"].(string); !ok || input != "hello" {
		t.Fatalf("input = %#v, want %q", p.lastChat.Raw["input"], "hello")
	}
}

func TestResponsesPreservesUnknownFields(t *testing.T) {
	resp := provider.OpenAIResponse{"id": "resp-test"}
	p := &stubProvider{response: &resp}
	rt, err := router.New([]config.ModelConfig{
		{PublicName: "gpt-4o-mini", Provider: "openai", UpstreamName: "gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	handler := newHandler(t, []config.KeyConfig{{Key: "sk-app-001"}}, rt, usage.New(100), p)
	body := bytes.NewBufferString(`{"model":"gpt-4o-mini","input":[{"type":"message","role":"user","content":"hi"}],"tools":[{"type":"function","name":"test"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
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
	if got, ok := p.lastChat.Raw["tools"].([]any); !ok || len(got) != 1 {
		t.Fatalf("tools = %#v, want one tool", p.lastChat.Raw["tools"])
	}
}
