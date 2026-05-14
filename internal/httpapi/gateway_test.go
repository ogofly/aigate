package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/provider"
	"aigate/internal/router"
	"aigate/internal/usage"
)

type gatewayTestStore struct {
	providers map[string]config.ProviderConfig
	settings  config.RoutingConfig
}

func (s *gatewayTestStore) GetProvider(_ context.Context, name string) (config.ProviderConfig, error) {
	providerCfg, ok := s.providers[name]
	if !ok {
		return config.ProviderConfig{}, errors.New("provider not found")
	}
	return providerCfg, nil
}

func (s *gatewayTestStore) GetRoutingSettings(context.Context) (config.RoutingConfig, error) {
	return s.settings, nil
}

type noopGatewayClient struct{}

func (noopGatewayClient) Chat(context.Context, config.ProviderConfig, *provider.ChatRequest, string) (*provider.OpenAIResponse, error) {
	panic("unexpected Chat call")
}

func (noopGatewayClient) ChatStream(context.Context, config.ProviderConfig, *provider.ChatRequest, string) (*provider.StreamResponse, error) {
	panic("unexpected ChatStream call")
}

func (noopGatewayClient) Messages(context.Context, config.ProviderConfig, *provider.ChatRequest, string) (*provider.AnthropicResponse, error) {
	panic("unexpected Messages call")
}

func (noopGatewayClient) MessagesStream(context.Context, config.ProviderConfig, *provider.ChatRequest, string) (*provider.StreamResponse, error) {
	panic("unexpected MessagesStream call")
}

func (noopGatewayClient) Embed(context.Context, config.ProviderConfig, provider.EmbeddingRequest, string) (*provider.EmbeddingResponse, error) {
	panic("unexpected Embed call")
}

func (noopGatewayClient) Responses(context.Context, config.ProviderConfig, *provider.ChatRequest, string) (*provider.OpenAIResponse, error) {
	panic("unexpected Responses call")
}

func (noopGatewayClient) ResponsesStream(context.Context, config.ProviderConfig, *provider.ChatRequest, string) (*provider.StreamResponse, error) {
	panic("unexpected ResponsesStream call")
}

func newGatewayExecutorForTest(t *testing.T, models []config.ModelConfig, recorder *usage.Recorder) *GatewayExecutor {
	t.Helper()
	rt, err := router.New(models)
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	if err := rt.Update(models, []config.ProviderConfig{
		{Name: "openai-a", Enabled: true},
		{Name: "openai-b", Enabled: true},
	}, config.RoutingConfig{Selection: "priority", FailoverEnabled: true, FailoverMaxAttempts: 2}); err != nil {
		t.Fatalf("router.Update() error = %v", err)
	}
	return &GatewayExecutor{
		router: rt,
		store: &gatewayTestStore{
			providers: map[string]config.ProviderConfig{
				"openai-a": {Name: "openai-a", BaseURL: "https://a.example/v1", APIKey: "secret", Enabled: true},
				"openai-b": {Name: "openai-b", BaseURL: "https://b.example/v1", APIKey: "secret", Enabled: true},
			},
			settings: config.RoutingConfig{Selection: "priority", FailoverEnabled: true, FailoverMaxAttempts: 2},
		},
		client:   noopGatewayClient{},
		recorder: recorder,
	}
}

func TestGatewayExecuteJSONRecordsSuccessUsage(t *testing.T) {
	recorder := usage.New(100)
	executor := newGatewayExecutorForTest(t, []config.ModelConfig{{
		ID:           "route-a",
		PublicName:   "gpt-4o-mini",
		Provider:     "openai-a",
		UpstreamName: "upstream-a",
		Enabled:      true,
	}}, recorder)
	principal := auth.Principal{Key: "sk-test", Name: "test-key"}

	result, errResp := executor.ExecuteJSON(context.Background(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), principal, time.Now(), gatewayJSONEndpoint{
		op:            "chat_completions",
		usageEndpoint: "chat.completions",
		model:         "gpt-4o-mini",
		call: func(context.Context, gatewayProviderClient, config.ProviderConfig, router.RouteTarget) (any, error) {
			resp := provider.OpenAIResponse{"usage": map[string]any{"prompt_tokens": float64(3), "completion_tokens": float64(4), "total_tokens": float64(7)}}
			return &resp, nil
		},
	})
	if errResp != nil {
		t.Fatalf("ExecuteJSON() error = %+v", errResp)
	}
	if result.status != http.StatusOK {
		t.Fatalf("status = %d, want %d", result.status, http.StatusOK)
	}
	summary, ok := recorder.SummaryByKey(principal.Key)
	if !ok {
		t.Fatal("SummaryByKey() missing summary")
	}
	if summary.RequestCount != 1 || summary.SuccessCount != 1 || summary.ErrorCount != 0 || summary.TotalTokens != 7 {
		t.Fatalf("summary = %+v, want one successful request with 7 tokens", summary)
	}
}

func TestGatewayExecuteJSONRetriesRetryableError(t *testing.T) {
	recorder := usage.New(100)
	executor := newGatewayExecutorForTest(t, []config.ModelConfig{
		{ID: "route-a", PublicName: "gpt-4o-mini", Provider: "openai-a", UpstreamName: "upstream-a", Priority: 0, Enabled: true},
		{ID: "route-b", PublicName: "gpt-4o-mini", Provider: "openai-b", UpstreamName: "upstream-b", Priority: 1, Enabled: true},
	}, recorder)
	principal := auth.Principal{Key: "sk-test"}
	var providers []string

	_, errResp := executor.ExecuteJSON(context.Background(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), principal, time.Now(), gatewayJSONEndpoint{
		op:            "chat_completions",
		usageEndpoint: "chat.completions",
		model:         "gpt-4o-mini",
		call: func(_ context.Context, _ gatewayProviderClient, _ config.ProviderConfig, target router.RouteTarget) (any, error) {
			providers = append(providers, target.ProviderName)
			if len(providers) == 1 {
				return nil, errors.New("upstream status 500")
			}
			resp := provider.OpenAIResponse{"id": "ok"}
			return &resp, nil
		},
	})
	if errResp != nil {
		t.Fatalf("ExecuteJSON() error = %+v", errResp)
	}
	if got := strings.Join(providers, ","); got != "openai-a,openai-b" {
		t.Fatalf("providers = %q, want openai-a,openai-b", got)
	}
	summary, ok := recorder.SummaryByKey(principal.Key)
	if !ok || summary.SuccessCount != 1 || summary.ErrorCount != 0 {
		t.Fatalf("summary = %+v, want one success only", summary)
	}
}

func TestGatewayExecuteJSONDoesNotRetryNonRetryableError(t *testing.T) {
	recorder := usage.New(100)
	executor := newGatewayExecutorForTest(t, []config.ModelConfig{
		{ID: "route-a", PublicName: "gpt-4o-mini", Provider: "openai-a", UpstreamName: "upstream-a", Priority: 0, Enabled: true},
		{ID: "route-b", PublicName: "gpt-4o-mini", Provider: "openai-b", UpstreamName: "upstream-b", Priority: 1, Enabled: true},
	}, recorder)
	principal := auth.Principal{Key: "sk-test"}
	calls := 0

	_, errResp := executor.ExecuteJSON(context.Background(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), principal, time.Now(), gatewayJSONEndpoint{
		op:            "chat_completions",
		usageEndpoint: "chat.completions",
		model:         "gpt-4o-mini",
		call: func(context.Context, gatewayProviderClient, config.ProviderConfig, router.RouteTarget) (any, error) {
			calls++
			return nil, errors.New("upstream status 400")
		},
	})
	if errResp == nil || errResp.status != http.StatusBadGateway {
		t.Fatalf("ExecuteJSON() error = %+v, want bad gateway", errResp)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	summary, ok := recorder.SummaryByKey(principal.Key)
	if !ok || summary.SuccessCount != 0 || summary.ErrorCount != 1 {
		t.Fatalf("summary = %+v, want one error", summary)
	}
}

func TestGatewayExecuteJSONRouteErrors(t *testing.T) {
	tests := []struct {
		name      string
		models    []config.ModelConfig
		principal auth.Principal
		wantCode  string
		wantHTTP  int
	}{
		{
			name:      "not found",
			models:    nil,
			principal: auth.Principal{Key: "sk-test", ModelAccess: "all"},
			wantCode:  "model_not_found",
			wantHTTP:  http.StatusBadRequest,
		},
		{
			name: "not allowed",
			models: []config.ModelConfig{{
				ID:           "route-a",
				PublicName:   "gpt-4o-mini",
				Provider:     "openai-a",
				UpstreamName: "upstream-a",
				Enabled:      true,
			}},
			principal: auth.Principal{Key: "sk-test", ModelAccess: "selected"},
			wantCode:  "model_not_allowed",
			wantHTTP:  http.StatusForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := newGatewayExecutorForTest(t, tt.models, usage.New(100))
			_, errResp := executor.ExecuteJSON(context.Background(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), tt.principal, time.Now(), gatewayJSONEndpoint{
				op:            "chat_completions",
				usageEndpoint: "chat.completions",
				model:         "gpt-4o-mini",
				call: func(context.Context, gatewayProviderClient, config.ProviderConfig, router.RouteTarget) (any, error) {
					t.Fatal("call should not run for route error")
					return nil, nil
				},
			})
			if errResp == nil || errResp.status != tt.wantHTTP || errResp.code != tt.wantCode {
				t.Fatalf("error = %+v, want status=%d code=%s", errResp, tt.wantHTTP, tt.wantCode)
			}
		})
	}
}

func TestGatewayExecuteStreamRecordsNon2xxUsage(t *testing.T) {
	recorder := usage.New(100)
	executor := newGatewayExecutorForTest(t, []config.ModelConfig{{
		ID:           "route-a",
		PublicName:   "gpt-4o-mini",
		Provider:     "openai-a",
		UpstreamName: "upstream-a",
		Enabled:      true,
	}}, recorder)
	principal := auth.Principal{Key: "sk-test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rr := httptest.NewRecorder()

	ok := executor.ExecuteStream(rr, req, principal, time.Now(), gatewayStreamEndpoint{
		op:            "chat_completions",
		usageEndpoint: "chat.completions",
		model:         "gpt-4o-mini",
		call: func(context.Context, gatewayProviderClient, config.ProviderConfig, router.RouteTarget) (*provider.StreamResponse, error) {
			return &provider.StreamResponse{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"bad"}`)),
			}, nil
		},
	})
	if !ok {
		t.Fatal("ExecuteStream() = false, want true for proxied upstream error")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "bad") {
		t.Fatalf("body = %q, want upstream body", rr.Body.String())
	}
	summary, ok := recorder.SummaryByKey(principal.Key)
	if !ok || summary.SuccessCount != 0 || summary.ErrorCount != 1 {
		t.Fatalf("summary = %+v, want one stream error", summary)
	}
}
