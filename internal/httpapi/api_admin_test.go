package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/httpapi"
	"aigate/internal/router"
	"aigate/internal/store"
	"aigate/internal/usage"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const (
	testAdminUser     = "admin"
	testAdminPass     = "pass"
	testSessionCookie = "aigate_admin_session"
)

type providerAPIResponse struct {
	Name             string `json:"name"`
	BaseURL          string `json:"base_url"`
	AnthropicBaseURL string `json:"anthropic_base_url"`
	AnthropicVersion string `json:"anthropic_version"`
	APIKey           string `json:"api_key"`
	APIKeyConfigured bool   `json:"api_key_configured"`
	APIKeyRef        string `json:"api_key_ref"`
	TimeoutSeconds   int    `json:"timeout"`
	Enabled          bool   `json:"enabled"`
}

func newTestAPIHandler(t *testing.T, keys []config.KeyConfig) http.Handler {
	t.Helper()
	handler, _ := newTestAPIHandlerWithStore(t, keys)
	return handler
}

func newTestAPIHandlerWithStore(t *testing.T, keys []config.KeyConfig) (http.Handler, *store.SQLiteStore) {
	t.Helper()
	os.Setenv("OPENAI_API_KEY", "test-secret")
	t.Cleanup(func() { os.Unsetenv("OPENAI_API_KEY") })

	models := []config.ModelConfig{{
		PublicName:   "gpt-4o",
		Provider:     "openai",
		UpstreamName: "gpt-4o",
	}}
	rt, err := router.New(models)
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeysIfEmpty() error = %v", err)
	}
	if err := sqliteStore.SeedProvidersIfEmpty(context.Background(), []config.ProviderConfig{{
		Name:           "openai",
		BaseURL:        "https://api.openai.com/v1",
		APIKeyRef:      "OPENAI_API_KEY",
		TimeoutSeconds: 60,
	}}); err != nil {
		t.Fatalf("SeedProvidersIfEmpty() error = %v", err)
	}
	if err := sqliteStore.SeedModelsIfEmpty(context.Background(), models); err != nil {
		t.Fatalf("SeedModelsIfEmpty() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	return newHandlerWithStore(keys, rt, usage.New(100), &stubProvider{}, sqliteStore), sqliteStore
}

// loginAndCookie performs admin login and returns a reusable cookie string.
func loginAndCookie(t *testing.T, handler http.Handler) string {
	t.Helper()
	body := strings.NewReader("username=" + testAdminUser + "&password=" + testAdminPass)
	req := httptest.NewRequest(http.MethodPost, "/admin/login", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", rr.Code)
	}

	cookies := rr.Result().Cookies()
	for _, c := range cookies {
		if c.Name == testSessionCookie {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("session cookie not found in login response")
	return ""
}

// userLoginAndCookie logs in as a non-admin user (via API key as password).
func userLoginAndCookie(t *testing.T, handler http.Handler, username, apiKey string) string {
	t.Helper()
	body := strings.NewReader("username=" + username + "&password=" + apiKey)
	req := httptest.NewRequest(http.MethodPost, "/admin/login", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("user login status = %d, want 303", rr.Code)
	}

	cookies := rr.Result().Cookies()
	for _, c := range cookies {
		if c.Name == testSessionCookie {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("session cookie not found in user login response")
	return ""
}

func apiGet(t *testing.T, handler http.Handler, url, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func apiPost(t *testing.T, handler http.Handler, url string, cookie string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload != nil {
		data, _ := json.Marshal(payload)
		body = bytes.NewReader(data)
	} else {
		body = bytes.NewReader([]byte{})
	}
	req := httptest.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func apiPostRaw(t *testing.T, handler http.Handler, url string, cookie string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func apiPut(t *testing.T, handler http.Handler, url string, cookie string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func apiDelete(t *testing.T, handler http.Handler, url, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, url, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func assertStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		body := rr.Body.String()
		t.Fatalf("status = %d, want %d, body = %s", rr.Code, want, body)
	}
}

func parseJSON(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("json.Unmarshal error = %v, body = %s", err, rr.Body.String())
	}
}

func parseErrorResponse(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	parseJSON(t, rr, &resp)
	return resp.Error.Code
}

// ---------------------------------------------------------------------------
// Auth: no session → 401/redirect
// ---------------------------------------------------------------------------

func TestAPIProvidersListNoSession(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	rr := apiGet(t, handler, "/api/admin/providers", "")
	// requireAdminSession redirects to /admin/login when no session
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect to login)", rr.Code)
	}
}

func TestAPIModelsListNoSession(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	rr := apiGet(t, handler, "/api/admin/models", "")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect to login)", rr.Code)
	}
}

func TestAPIKeysListNoSession(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	rr := apiGet(t, handler, "/api/admin/keys", "")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect to login)", rr.Code)
	}
}

func TestAPICreateNoSession(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	rr := apiPost(t, handler, "/api/admin/providers", "", map[string]string{"name": "test", "base_url": "http://x"})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect to login)", rr.Code)
	}
}

func TestAPIDeleteNoSession(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	rr := apiDelete(t, handler, "/api/admin/providers/test", "")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect to login)", rr.Code)
	}
}

func TestAPIPutNoSession(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	rr := apiPut(t, handler, "/api/admin/providers/test", "", map[string]string{"base_url": "http://x"})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect to login)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Providers: Full CRUD
// ---------------------------------------------------------------------------

func TestAPIProvidersCreateAndList(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	// Create a provider
	payload := map[string]any{
		"name":     "test-provider",
		"base_url": "https://api.test.com/v1",
		"api_key":  "test-key-123",
		"timeout":  30,
	}
	rr := apiPost(t, handler, "/api/admin/providers", cookie, payload)
	assertStatus(t, rr, http.StatusOK)

	var successResp struct {
		Ok      bool   `json:"ok"`
		Message string `json:"message"`
	}
	parseJSON(t, rr, &successResp)
	if !successResp.Ok {
		t.Fatal("expected ok=true")
	}
	if successResp.Message != "provider created" {
		t.Fatalf("message = %q, want 'provider created'", successResp.Message)
	}

	// List providers — should include the seeded "openai" + new one
	rr = apiGet(t, handler, "/api/admin/providers", cookie)
	assertStatus(t, rr, http.StatusOK)

	var providers []providerAPIResponse
	parseJSON(t, rr, &providers)
	if len(providers) != 2 {
		t.Fatalf("len(providers) = %d, want 2", len(providers))
	}

	// Verify the created provider
	for _, p := range providers {
		if p.Name == "test-provider" {
			if p.BaseURL != "https://api.test.com/v1" {
				t.Fatalf("base_url = %q, want https://api.test.com/v1", p.BaseURL)
			}
			if p.TimeoutSeconds != 30 {
				t.Fatalf("timeout = %d, want 30", p.TimeoutSeconds)
			}
			if p.APIKey != "" {
				t.Fatalf("api_key = %q, want hidden", p.APIKey)
			}
			if !p.APIKeyConfigured {
				t.Fatal("api_key_configured = false, want true")
			}
			return
		}
	}
	t.Fatal("created provider not found in list")
}

func TestAPIProviderGet(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	// Get seeded provider
	rr := apiGet(t, handler, "/api/admin/providers/openai", cookie)
	assertStatus(t, rr, http.StatusOK)

	var provider providerAPIResponse
	parseJSON(t, rr, &provider)
	if provider.Name != "openai" {
		t.Fatalf("name = %q, want openai", provider.Name)
	}
	if provider.APIKey != "" {
		t.Fatalf("api_key = %q, want hidden", provider.APIKey)
	}
}

func TestAPIProviderGetNotFound(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiGet(t, handler, "/api/admin/providers/nonexistent", cookie)
	assertStatus(t, rr, http.StatusNotFound)
}

func TestAPIProviderCreateValidation(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	tests := []struct {
		name    string
		payload map[string]any
		wantErr string
	}{
		{
			name:    "missing name",
			payload: map[string]any{"base_url": "https://api.test.com/v1", "api_key": "k"},
			wantErr: "invalid_request",
		},
		{
			name:    "missing base_url",
			payload: map[string]any{"name": "test", "api_key": "k"},
			wantErr: "invalid_request",
		},
		{
			name:    "missing api_key and api_key_ref",
			payload: map[string]any{"name": "test", "base_url": "https://api.test.com/v1"},
			wantErr: "invalid_request",
		},
		{
			name:    "invalid JSON",
			payload: nil,
			wantErr: "invalid_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := apiPost(t, handler, "/api/admin/providers", cookie, tt.payload)
			assertStatus(t, rr, http.StatusBadRequest)
			code := parseErrorResponse(t, rr)
			if code != tt.wantErr {
				t.Fatalf("error code = %q, want %q", code, tt.wantErr)
			}
		})
	}
}

func TestAPIProviderCreateRejectsTrailingJSON(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiPostRaw(t, handler, "/api/admin/providers", cookie, `{"name":"test","base_url":"https://api.test.com/v1","api_key":"k"}{"name":"extra"}`)
	assertStatus(t, rr, http.StatusBadRequest)

	code := parseErrorResponse(t, rr)
	if code != "invalid_request" {
		t.Fatalf("error code = %q, want invalid_request", code)
	}
}

func TestAPIProviderUpdate(t *testing.T) {
	handler, sqliteStore := newTestAPIHandlerWithStore(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	// First create a provider
	createPayload := map[string]any{
		"name":     "updatable",
		"base_url": "https://api.old.com/v1",
		"api_key":  "old-key",
		"timeout":  60,
	}
	rr := apiPost(t, handler, "/api/admin/providers", cookie, createPayload)
	assertStatus(t, rr, http.StatusOK)

	// Update: change base_url and timeout
	updatePayload := map[string]any{
		"base_url": "https://api.new.com/v2",
		"timeout":  120,
	}
	rr = apiPut(t, handler, "/api/admin/providers/updatable", cookie, updatePayload)
	assertStatus(t, rr, http.StatusOK)

	var successResp struct {
		Ok bool `json:"ok"`
	}
	parseJSON(t, rr, &successResp)
	if !successResp.Ok {
		t.Fatal("expected ok=true")
	}

	// Verify update
	rr = apiGet(t, handler, "/api/admin/providers/updatable", cookie)
	assertStatus(t, rr, http.StatusOK)

	var provider providerAPIResponse
	parseJSON(t, rr, &provider)
	if provider.BaseURL != "https://api.new.com/v2" {
		t.Fatalf("base_url = %q, want https://api.new.com/v2", provider.BaseURL)
	}
	if provider.TimeoutSeconds != 120 {
		t.Fatalf("timeout = %d, want 120", provider.TimeoutSeconds)
	}
	if provider.APIKey != "" {
		t.Fatalf("api_key = %q, want hidden", provider.APIKey)
	}
	stored, err := sqliteStore.GetProvider(context.Background(), "updatable")
	if err != nil {
		t.Fatalf("GetProvider() error = %v", err)
	}
	if stored.APIKey != "old-key" {
		t.Fatalf("stored api_key = %q, want old-key (preserved)", stored.APIKey)
	}
}

func TestAPIProviderUpdateNotFound(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiPut(t, handler, "/api/admin/providers/nonexistent", cookie, map[string]any{"base_url": "http://x"})
	assertStatus(t, rr, http.StatusNotFound)
}

func TestAPIProviderUpdateMissingBaseURL(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiPut(t, handler, "/api/admin/providers/openai", cookie, map[string]any{})
	assertStatus(t, rr, http.StatusBadRequest)
}

func TestAPIProviderDelete(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	// Create a provider to delete
	createPayload := map[string]any{
		"name":     "to-delete",
		"base_url": "https://api.test.com/v1",
		"api_key":  "k",
	}
	rr := apiPost(t, handler, "/api/admin/providers", cookie, createPayload)
	assertStatus(t, rr, http.StatusOK)

	// Delete it
	rr = apiDelete(t, handler, "/api/admin/providers/to-delete", cookie)
	assertStatus(t, rr, http.StatusOK)

	var successResp struct {
		Ok bool `json:"ok"`
	}
	parseJSON(t, rr, &successResp)
	if !successResp.Ok {
		t.Fatal("expected ok=true")
	}

	// Verify it's gone
	rr = apiGet(t, handler, "/api/admin/providers/to-delete", cookie)
	assertStatus(t, rr, http.StatusNotFound)
}

func TestAPIProviderDeleteBlockedByModel(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	// The seeded "openai" provider has a model referencing it
	rr := apiDelete(t, handler, "/api/admin/providers/openai", cookie)
	assertStatus(t, rr, http.StatusBadRequest)

	code := parseErrorResponse(t, rr)
	if code != "api_provider_delete_error" {
		t.Fatalf("error code = %q, want api_provider_delete_error", code)
	}
}

func TestAPIProviderDeleteNotFound(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	// Deleting a nonexistent provider is idempotent (SQLite doesn't error).
	// The handler returns success regardless.
	rr := apiDelete(t, handler, "/api/admin/providers/nonexistent", cookie)
	assertStatus(t, rr, http.StatusOK)
}

// ---------------------------------------------------------------------------
// Models: Full CRUD
// ---------------------------------------------------------------------------

func TestAPIModelsList(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiGet(t, handler, "/api/admin/models", cookie)
	assertStatus(t, rr, http.StatusOK)

	var models []config.ModelConfig
	parseJSON(t, rr, &models)
	// seeded model should be present
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].PublicName != "gpt-4o" {
		t.Fatalf("model name = %q, want gpt-4o", models[0].PublicName)
	}
}

func TestAPIModelCreateAndDelete(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	// Create a model
	createPayload := map[string]any{
		"public_name":   "my-model",
		"provider":      "openai",
		"upstream_name": "gpt-4",
	}
	rr := apiPost(t, handler, "/api/admin/models", cookie, createPayload)
	assertStatus(t, rr, http.StatusOK)

	var successResp struct {
		Ok bool `json:"ok"`
	}
	parseJSON(t, rr, &successResp)
	if !successResp.Ok {
		t.Fatal("expected ok=true")
	}

	// Verify in list
	rr = apiGet(t, handler, "/api/admin/models", cookie)
	assertStatus(t, rr, http.StatusOK)

	var models []config.ModelConfig
	parseJSON(t, rr, &models)
	found := false
	for _, m := range models {
		if m.PublicName == "my-model" {
			found = true
			if m.Provider != "openai" {
				t.Fatalf("provider = %q, want openai", m.Provider)
			}
			if m.UpstreamName != "gpt-4" {
				t.Fatalf("upstream_name = %q, want gpt-4", m.UpstreamName)
			}
			break
		}
	}
	if !found {
		t.Fatal("created model not found in list")
	}

	// Delete the model
	rr = apiDelete(t, handler, "/api/admin/models/my-model", cookie)
	assertStatus(t, rr, http.StatusOK)

	// Verify deleted
	rr = apiGet(t, handler, "/api/admin/models", cookie)
	assertStatus(t, rr, http.StatusOK)

	var modelsAfter []config.ModelConfig
	parseJSON(t, rr, &modelsAfter)
	if len(modelsAfter) != 1 {
		t.Fatalf("len(models) = %d, want 1 after delete", len(modelsAfter))
	}
}

func TestAPIModelDeleteAmbiguousPublicNameRequiresRouteID(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	for _, upstream := range []string{"gpt-4-a", "gpt-4-b"} {
		rr := apiPost(t, handler, "/api/admin/models", cookie, map[string]any{
			"public_name":   "shared-model",
			"provider":      "openai",
			"upstream_name": upstream,
		})
		assertStatus(t, rr, http.StatusOK)
	}

	rr := apiDelete(t, handler, "/api/admin/models/shared-model", cookie)
	assertStatus(t, rr, http.StatusBadRequest)

	code := parseErrorResponse(t, rr)
	if code != "api_model_delete_error" {
		t.Fatalf("error code = %q, want api_model_delete_error", code)
	}
}

func TestAPIModelDeleteByRouteIDOnlyDeletesOneRoute(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	for _, upstream := range []string{"gpt-4-a", "gpt-4-b"} {
		rr := apiPost(t, handler, "/api/admin/models", cookie, map[string]any{
			"public_name":   "shared-model",
			"provider":      "openai",
			"upstream_name": upstream,
		})
		assertStatus(t, rr, http.StatusOK)
	}

	rr := apiGet(t, handler, "/api/admin/models", cookie)
	assertStatus(t, rr, http.StatusOK)
	var models []config.ModelConfig
	parseJSON(t, rr, &models)

	routeID := ""
	for _, model := range models {
		if model.PublicName == "shared-model" && model.UpstreamName == "gpt-4-a" {
			routeID = model.ID
			break
		}
	}
	if routeID == "" {
		t.Fatal("route id for shared-model/gpt-4-a not found")
	}

	rr = apiDelete(t, handler, "/api/admin/models/"+routeID, cookie)
	assertStatus(t, rr, http.StatusOK)

	rr = apiGet(t, handler, "/api/admin/models", cookie)
	assertStatus(t, rr, http.StatusOK)
	models = nil
	parseJSON(t, rr, &models)
	remainingSharedRoutes := 0
	for _, model := range models {
		if model.PublicName == "shared-model" {
			remainingSharedRoutes++
			if model.UpstreamName != "gpt-4-b" {
				t.Fatalf("remaining shared upstream = %q, want gpt-4-b", model.UpstreamName)
			}
		}
	}
	if remainingSharedRoutes != 1 {
		t.Fatalf("remaining shared routes = %d, want 1", remainingSharedRoutes)
	}
}

func TestAPIModelCreateValidation(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	tests := []struct {
		name    string
		payload map[string]any
	}{
		{"missing public_name", map[string]any{"provider": "openai", "upstream_name": "gpt-4"}},
		{"missing provider", map[string]any{"public_name": "m", "upstream_name": "gpt-4"}},
		{"missing upstream_name", map[string]any{"public_name": "m", "provider": "openai"}},
		{"missing all", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := apiPost(t, handler, "/api/admin/models", cookie, tt.payload)
			assertStatus(t, rr, http.StatusBadRequest)
		})
	}
}

func TestAPIModelCreateUnknownProvider(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiPost(t, handler, "/api/admin/models", cookie, map[string]any{
		"public_name":   "bad-model",
		"provider":      "nonexistent",
		"upstream_name": "gpt-4",
	})
	assertStatus(t, rr, http.StatusBadRequest)

	code := parseErrorResponse(t, rr)
	if code != "invalid_request" {
		t.Fatalf("error code = %q, want invalid_request", code)
	}
}

func TestAPIModelUpdateRenameDeletesOldName(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiPut(t, handler, "/api/admin/models/gpt-4o", cookie, map[string]any{
		"public_name":   "renamed-model",
		"provider":      "openai",
		"upstream_name": "gpt-4.1",
	})
	assertStatus(t, rr, http.StatusOK)

	rr = apiGet(t, handler, "/api/admin/models", cookie)
	assertStatus(t, rr, http.StatusOK)

	var models []config.ModelConfig
	parseJSON(t, rr, &models)
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].PublicName != "renamed-model" {
		t.Fatalf("public_name = %q, want renamed-model", models[0].PublicName)
	}
	if models[0].UpstreamName != "gpt-4.1" {
		t.Fatalf("upstream_name = %q, want gpt-4.1", models[0].UpstreamName)
	}
}

func TestAPIModelUpdateNotFound(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiPut(t, handler, "/api/admin/models/missing-model", cookie, map[string]any{
		"provider":      "openai",
		"upstream_name": "gpt-4",
	})
	assertStatus(t, rr, http.StatusNotFound)
}

func TestAPIModelUpdateAllowsDuplicatePublicNameForDistinctRoute(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiPost(t, handler, "/api/admin/models", cookie, map[string]any{
		"public_name":   "existing-model",
		"provider":      "openai",
		"upstream_name": "gpt-4",
	})
	assertStatus(t, rr, http.StatusOK)

	rr = apiPut(t, handler, "/api/admin/models/gpt-4o", cookie, map[string]any{
		"public_name":   "existing-model",
		"provider":      "openai",
		"upstream_name": "gpt-4.1",
	})
	assertStatus(t, rr, http.StatusOK)

	rr = apiGet(t, handler, "/api/admin/models", cookie)
	assertStatus(t, rr, http.StatusOK)

	var models []config.ModelConfig
	parseJSON(t, rr, &models)
	count := 0
	for _, model := range models {
		if model.PublicName == "existing-model" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("duplicate public model route count = %d, want 2", count)
	}
}

func TestAPIModelUpdateAmbiguousPublicNameRequiresRouteID(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	for _, upstream := range []string{"gpt-4-a", "gpt-4-b"} {
		rr := apiPost(t, handler, "/api/admin/models", cookie, map[string]any{
			"public_name":   "shared-model",
			"provider":      "openai",
			"upstream_name": upstream,
		})
		assertStatus(t, rr, http.StatusOK)
	}

	rr := apiPut(t, handler, "/api/admin/models/shared-model", cookie, map[string]any{
		"priority": 10,
	})
	assertStatus(t, rr, http.StatusBadRequest)

	code := parseErrorResponse(t, rr)
	if code != "ambiguous_model_route" {
		t.Fatalf("error code = %q, want ambiguous_model_route", code)
	}
}

// ---------------------------------------------------------------------------
// Keys: Full CRUD + ownership
// ---------------------------------------------------------------------------

func TestAPIKeysList(t *testing.T) {
	keys := []config.KeyConfig{
		{Key: "sk-1", Name: "key1", Owner: "user1", Purpose: "testing"},
		{Key: "sk-2", Name: "key2", Owner: "user2", Purpose: "prod"},
	}
	handler := newTestAPIHandler(t, keys)
	cookie := loginAndCookie(t, handler)

	rr := apiGet(t, handler, "/api/admin/keys", cookie)
	assertStatus(t, rr, http.StatusOK)

	var result []config.KeyConfig
	parseJSON(t, rr, &result)
	if len(result) != 2 {
		t.Fatalf("len(keys) = %d, want 2", len(result))
	}
}

func TestAPIKeyCreateAdmin(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	payload := map[string]any{
		"key":     "sk-new-key-123",
		"name":    "new-key",
		"owner":   "user1",
		"purpose": "testing",
	}
	rr := apiPost(t, handler, "/api/admin/keys", cookie, payload)
	assertStatus(t, rr, http.StatusOK)

	var successResp struct {
		Ok bool `json:"ok"`
	}
	parseJSON(t, rr, &successResp)
	if !successResp.Ok {
		t.Fatal("expected ok=true")
	}

	// Verify key is in the list
	rr = apiGet(t, handler, "/api/admin/keys", cookie)
	assertStatus(t, rr, http.StatusOK)

	var keys []config.KeyConfig
	parseJSON(t, rr, &keys)
	found := false
	for _, k := range keys {
		if k.Key == "sk-new-key-123" {
			found = true
			if k.Name != "new-key" {
				t.Fatalf("name = %q, want new-key", k.Name)
			}
			if k.Owner != "user1" {
				t.Fatalf("owner = %q, want user1", k.Owner)
			}
			if k.Purpose != "testing" {
				t.Fatalf("purpose = %q, want testing", k.Purpose)
			}
			break
		}
	}
	if !found {
		t.Fatal("created key not found in list")
	}
}

func TestAPIKeyCreateDuplicate(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-existing"}})
	cookie := loginAndCookie(t, handler)

	rr := apiPost(t, handler, "/api/admin/keys", cookie, map[string]any{
		"key":  "sk-existing",
		"name": "dup",
	})
	assertStatus(t, rr, http.StatusConflict)

	code := parseErrorResponse(t, rr)
	if code != "api_key_duplicate" {
		t.Fatalf("error code = %q, want api_key_duplicate", code)
	}
}

func TestAPIKeyCreateMissingKey(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiPost(t, handler, "/api/admin/keys", cookie, map[string]any{
		"name": "no-key",
	})
	assertStatus(t, rr, http.StatusBadRequest)
}

func TestAPIKeyCreateUserForcesOwnOwner(t *testing.T) {
	keys := []config.KeyConfig{{Key: "sk-1"}}
	handler := newTestAPIHandler(t, keys)

	// First, create a key owned by "testuser" so they can login
	// We need to insert directly into the store
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("NewSQLite error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeys error = %v", err)
	}

	// Upsert a key with owner for the user to login
	if err := sqliteStore.UpsertAuthKey(context.Background(), config.KeyConfig{
		Key:   "sk-user1-pass",
		Name:  "user1 pass key",
		Owner: "testuser",
	}); err != nil {
		t.Fatalf("UpsertAuthKey error = %v", err)
	}

	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "gpt-4o",
		Provider:     "openai",
		UpstreamName: "gpt-4o",
	}})
	if err != nil {
		t.Fatalf("router.New error = %v", err)
	}

	os.Setenv("OPENAI_API_KEY", "test-secret")
	defer os.Unsetenv("OPENAI_API_KEY")

	handler = httpapi.NewWithClient(
		auth.New(keys),
		config.AdminConfig{Username: "admin", Password: "pass"},
		&stubProvider{}, rt, usage.New(100), sqliteStore, []string{"openai"},
	)

	// Login as testuser
	userCookie := userLoginAndCookie(t, handler, "testuser", "sk-user1-pass")

	// Try to create a key with a different owner — should be forced to own username
	rr := apiPost(t, handler, "/api/admin/keys", userCookie, map[string]any{
		"key":   "sk-user1-new",
		"name":  "user1 key",
		"owner": "hacker", // should be ignored
	})
	assertStatus(t, rr, http.StatusOK)

	// Verify the key's owner is testuser, not hacker
	rr = apiGet(t, handler, "/api/admin/keys", userCookie)
	assertStatus(t, rr, http.StatusOK)

	var result []config.KeyConfig
	parseJSON(t, rr, &result)
	for _, k := range result {
		if k.Key == "sk-user1-new" {
			if k.Owner != "testuser" {
				t.Fatalf("owner = %q, want testuser (forced)", k.Owner)
			}
			return
		}
	}
	t.Fatal("created key not found in user's key list")
}

func TestAPIKeyCreateUserCannotEscalateSelectedModelAccess(t *testing.T) {
	models := []config.ModelConfig{
		{ID: "mrt_allowed", PublicName: "allowed-model", Provider: "openai", UpstreamName: "allowed-upstream", Enabled: true},
		{ID: "mrt_denied", PublicName: "denied-model", Provider: "openai", UpstreamName: "denied-upstream", Enabled: true},
	}
	rt, err := router.New(models)
	if err != nil {
		t.Fatalf("router.New error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("NewSQLite error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: true}); err != nil {
		t.Fatalf("UpsertProvider error = %v", err)
	}
	for _, model := range models {
		if err := sqliteStore.UpsertModel(context.Background(), model); err != nil {
			t.Fatalf("UpsertModel error = %v", err)
		}
	}
	keys := []config.KeyConfig{{Key: "sk-user-pass", Owner: "testuser", ModelAccess: "selected", ModelRouteIDs: []string{"mrt_allowed"}}}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeys error = %v", err)
	}
	handler := httpapi.NewWithClient(auth.New(keys), config.AdminConfig{Username: "admin", Password: "pass"}, &stubProvider{}, rt, usage.New(100), sqliteStore, []string{"openai"})
	userCookie := userLoginAndCookie(t, handler, "testuser", "sk-user-pass")

	rr := apiPost(t, handler, "/api/admin/keys", userCookie, map[string]any{
		"key":          "sk-escalated-all",
		"model_access": "all",
	})
	assertStatus(t, rr, http.StatusForbidden)

	rr = apiPost(t, handler, "/api/admin/keys", userCookie, map[string]any{
		"key":             "sk-escalated-route",
		"model_access":    "selected",
		"model_route_ids": []string{"mrt_denied"},
	})
	assertStatus(t, rr, http.StatusForbidden)

	rr = apiPost(t, handler, "/api/admin/keys", userCookie, map[string]any{
		"key":             "sk-selected-child",
		"model_access":    "selected",
		"model_route_ids": []string{"mrt_allowed"},
	})
	assertStatus(t, rr, http.StatusOK)
}

func TestAPIKeyUpdateUserCannotEscalateSelectedModelAccess(t *testing.T) {
	models := []config.ModelConfig{
		{ID: "mrt_allowed", PublicName: "allowed-model", Provider: "openai", UpstreamName: "allowed-upstream", Enabled: true},
		{ID: "mrt_denied", PublicName: "denied-model", Provider: "openai", UpstreamName: "denied-upstream", Enabled: true},
	}
	rt, err := router.New(models)
	if err != nil {
		t.Fatalf("router.New error = %v", err)
	}
	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("NewSQLite error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	if err := sqliteStore.UpsertProvider(context.Background(), config.ProviderConfig{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "secret", TimeoutSeconds: 60, Enabled: true}); err != nil {
		t.Fatalf("UpsertProvider error = %v", err)
	}
	for _, model := range models {
		if err := sqliteStore.UpsertModel(context.Background(), model); err != nil {
			t.Fatalf("UpsertModel error = %v", err)
		}
	}
	keys := []config.KeyConfig{
		{Key: "sk-user-pass", Owner: "testuser", ModelAccess: "selected", ModelRouteIDs: []string{"mrt_allowed"}},
		{Key: "sk-child", Owner: "testuser", ModelAccess: "selected", ModelRouteIDs: []string{"mrt_allowed"}},
	}
	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeys error = %v", err)
	}
	handler := httpapi.NewWithClient(auth.New(keys), config.AdminConfig{Username: "admin", Password: "pass"}, &stubProvider{}, rt, usage.New(100), sqliteStore, []string{"openai"})
	userCookie := userLoginAndCookie(t, handler, "testuser", "sk-user-pass")

	rr := apiPut(t, handler, "/api/admin/keys/sk-child", userCookie, map[string]any{
		"name":         "child",
		"model_access": "all",
	})
	assertStatus(t, rr, http.StatusForbidden)
}

func TestAPIKeyDelete(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiDelete(t, handler, "/api/admin/keys/sk-1", cookie)
	assertStatus(t, rr, http.StatusOK)

	var successResp struct {
		Ok bool `json:"ok"`
	}
	parseJSON(t, rr, &successResp)
	if !successResp.Ok {
		t.Fatal("expected ok=true")
	}

	// Verify deleted
	rr = apiGet(t, handler, "/api/admin/keys", cookie)
	assertStatus(t, rr, http.StatusOK)

	var keys []config.KeyConfig
	parseJSON(t, rr, &keys)
	if len(keys) != 0 {
		t.Fatalf("len(keys) = %d, want 0 after delete", len(keys))
	}
}

func TestAPIKeyDeleteNotFound(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	// Deleting a nonexistent key is idempotent (SQLite doesn't error).
	rr := apiDelete(t, handler, "/api/admin/keys/nonexistent", cookie)
	assertStatus(t, rr, http.StatusOK)
}

func TestAPIKeyDeleteOwnerCheck(t *testing.T) {
	// Create two keys owned by different users
	keys := []config.KeyConfig{
		{Key: "sk-user1", Name: "user1 key", Owner: "user1"},
		{Key: "sk-user1-extra", Name: "user1 extra key", Owner: "user1"},
		{Key: "sk-user2", Name: "user2 key", Owner: "user2"},
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("NewSQLite error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeys error = %v", err)
	}

	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "gpt-4o",
		Provider:     "openai",
		UpstreamName: "gpt-4o",
	}})
	if err != nil {
		t.Fatalf("router.New error = %v", err)
	}

	os.Setenv("OPENAI_API_KEY", "test-secret")
	defer os.Unsetenv("OPENAI_API_KEY")

	handler := httpapi.NewWithClient(
		auth.New(keys),
		config.AdminConfig{Username: "admin", Password: "pass"},
		&stubProvider{}, rt, usage.New(100), sqliteStore, []string{"openai"},
	)

	// user1 logs in
	user1Cookie := userLoginAndCookie(t, handler, "user1", "sk-user1")

	// user1 tries to delete user2's key — should be forbidden
	rr := apiDelete(t, handler, "/api/admin/keys/sk-user2", user1Cookie)
	assertStatus(t, rr, http.StatusForbidden)

	// user1 deletes own key — should succeed
	rr = apiDelete(t, handler, "/api/admin/keys/sk-user1", user1Cookie)
	assertStatus(t, rr, http.StatusOK)
}

func TestAPIKeyDeleteUserCannotDeleteLastOwnKey(t *testing.T) {
	keys := []config.KeyConfig{
		{Key: "sk-user1", Name: "user1 key", Owner: "user1"},
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("NewSQLite error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeys error = %v", err)
	}

	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "gpt-4o",
		Provider:     "openai",
		UpstreamName: "gpt-4o",
	}})
	if err != nil {
		t.Fatalf("router.New error = %v", err)
	}

	os.Setenv("OPENAI_API_KEY", "test-secret")
	defer os.Unsetenv("OPENAI_API_KEY")

	handler := httpapi.NewWithClient(
		auth.New(keys),
		config.AdminConfig{Username: "admin", Password: "pass"},
		&stubProvider{}, rt, usage.New(100), sqliteStore, []string{"openai"},
	)
	user1Cookie := userLoginAndCookie(t, handler, "user1", "sk-user1")

	rr := apiDelete(t, handler, "/api/admin/keys/sk-user1", user1Cookie)
	assertStatus(t, rr, http.StatusConflict)

	got, err := sqliteStore.GetAuthKey(context.Background(), "sk-user1")
	if err != nil {
		t.Fatalf("GetAuthKey() error = %v", err)
	}
	if got.Key != "sk-user1" {
		t.Fatalf("key = %q, want sk-user1", got.Key)
	}
}

func TestAPIKeyDeleteAdminCanDeleteAny(t *testing.T) {
	keys := []config.KeyConfig{
		{Key: "sk-user1", Name: "user1 key", Owner: "user1"},
	}

	sqliteStore, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("NewSQLite error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	if err := sqliteStore.SeedAuthKeysIfEmpty(context.Background(), keys); err != nil {
		t.Fatalf("SeedAuthKeys error = %v", err)
	}

	rt, err := router.New([]config.ModelConfig{{
		PublicName:   "gpt-4o",
		Provider:     "openai",
		UpstreamName: "gpt-4o",
	}})
	if err != nil {
		t.Fatalf("router.New error = %v", err)
	}

	os.Setenv("OPENAI_API_KEY", "test-secret")
	defer os.Unsetenv("OPENAI_API_KEY")

	handler := httpapi.NewWithClient(
		auth.New(keys),
		config.AdminConfig{Username: "admin", Password: "pass"},
		&stubProvider{}, rt, usage.New(100), sqliteStore, []string{"openai"},
	)

	cookie := loginAndCookie(t, handler)

	// Admin deletes user1's key
	rr := apiDelete(t, handler, "/api/admin/keys/sk-user1", cookie)
	assertStatus(t, rr, http.StatusOK)
}

// ---------------------------------------------------------------------------
// Content-Type verification
// ---------------------------------------------------------------------------

func TestAPIReturnsJSONContentType(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	endpoints := []string{
		"/api/admin/providers",
		"/api/admin/models",
		"/api/admin/keys",
		"/api/admin/routing",
	}

	for _, url := range endpoints {
		t.Run(url, func(t *testing.T) {
			rr := apiGet(t, handler, url, cookie)
			assertStatus(t, rr, http.StatusOK)
			ct := rr.Header().Get("Content-Type")
			if ct != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", ct)
			}
		})
	}
}

func TestAPIRoutingSettingsUpdate(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	rr := apiPut(t, handler, "/api/admin/routing", cookie, map[string]any{
		"selection":             "weight",
		"failover_enabled":      false,
		"failover_max_attempts": 3,
	})
	assertStatus(t, rr, http.StatusOK)

	rr = apiGet(t, handler, "/api/admin/routing", cookie)
	assertStatus(t, rr, http.StatusOK)

	var settings config.RoutingConfig
	parseJSON(t, rr, &settings)
	if settings.Selection != "weight" {
		t.Fatalf("selection = %q, want weight", settings.Selection)
	}
	if settings.FailoverEnabled {
		t.Fatal("failover_enabled = true, want false")
	}
	if settings.FailoverMaxAttempts != 3 {
		t.Fatalf("failover_max_attempts = %d, want 3", settings.FailoverMaxAttempts)
	}
}

func TestAPIErrorReturnsJSONContentType(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	// Request that returns an error
	rr := apiPost(t, handler, "/api/admin/providers", cookie, nil)
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("error Content-Type = %q, want application/json", ct)
	}
}

// ---------------------------------------------------------------------------
// Full workflow: Create Provider → Create Model → Create Key → Delete
// ---------------------------------------------------------------------------

func TestAPIFullWorkflow(t *testing.T) {
	handler := newTestAPIHandler(t, []config.KeyConfig{{Key: "sk-1"}})
	cookie := loginAndCookie(t, handler)

	// 1. Create provider
	rr := apiPost(t, handler, "/api/admin/providers", cookie, map[string]any{
		"name":     "workflow-provider",
		"base_url": "https://api.workflow.test/v1",
		"api_key":  "wf-key",
		"timeout":  45,
	})
	assertStatus(t, rr, http.StatusOK)

	// 2. Create model referencing that provider
	rr = apiPost(t, handler, "/api/admin/models", cookie, map[string]any{
		"public_name":   "workflow-model",
		"provider":      "workflow-provider",
		"upstream_name": "wf-upstream",
	})
	assertStatus(t, rr, http.StatusOK)

	// 3. Create key
	rr = apiPost(t, handler, "/api/admin/keys", cookie, map[string]any{
		"key":     "sk-wf-key",
		"name":    "workflow key",
		"purpose": "e2e test",
	})
	assertStatus(t, rr, http.StatusOK)

	// 4. Verify all exist
	rr = apiGet(t, handler, "/api/admin/providers", cookie)
	assertStatus(t, rr, http.StatusOK)

	var providers []providerAPIResponse
	parseJSON(t, rr, &providers)
	hasProvider := false
	for _, p := range providers {
		if p.Name == "workflow-provider" {
			hasProvider = true
			if p.TimeoutSeconds != 45 {
				t.Fatalf("provider timeout = %d, want 45", p.TimeoutSeconds)
			}
		}
	}
	if !hasProvider {
		t.Fatal("workflow provider not found")
	}

	rr = apiGet(t, handler, "/api/admin/models", cookie)
	assertStatus(t, rr, http.StatusOK)

	var models []config.ModelConfig
	parseJSON(t, rr, &models)
	hasModel := false
	for _, m := range models {
		if m.PublicName == "workflow-model" {
			hasModel = true
		}
	}
	if !hasModel {
		t.Fatal("workflow model not found")
	}

	rr = apiGet(t, handler, "/api/admin/keys", cookie)
	assertStatus(t, rr, http.StatusOK)

	var keys []config.KeyConfig
	parseJSON(t, rr, &keys)
	hasKey := false
	for _, k := range keys {
		if k.Key == "sk-wf-key" {
			hasKey = true
		}
	}
	if !hasKey {
		t.Fatal("workflow key not found")
	}

	// 5. Delete model (required before deleting provider)
	rr = apiDelete(t, handler, "/api/admin/models/workflow-model", cookie)
	assertStatus(t, rr, http.StatusOK)

	// 6. Delete provider
	rr = apiDelete(t, handler, "/api/admin/providers/workflow-provider", cookie)
	assertStatus(t, rr, http.StatusOK)

	// 7. Delete key
	rr = apiDelete(t, handler, "/api/admin/keys/sk-wf-key", cookie)
	assertStatus(t, rr, http.StatusOK)

	// 8. Verify clean state
	rr = apiGet(t, handler, "/api/admin/models", cookie)
	assertStatus(t, rr, http.StatusOK)

	var modelsClean []config.ModelConfig
	parseJSON(t, rr, &modelsClean)
	for _, m := range modelsClean {
		if m.PublicName == "workflow-model" {
			t.Fatal("workflow model should be deleted")
		}
	}
}
