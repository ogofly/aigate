package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/provider"
	"aigate/internal/router"
	"aigate/internal/store"
	"aigate/internal/usage"
)

const maxRequestBodyBytes int64 = 32 << 20

type Handler struct {
	auth          *auth.Auth
	admin         config.AdminConfig
	client        gatewayProviderClient
	executor      *GatewayExecutor
	router        *router.Router
	usage         *usage.Recorder
	store         *store.SQLiteStore
	providerMu    sync.RWMutex
	providerNames []string
	sessions      *adminSessionStore
	mux           *http.ServeMux
}

func New(authenticator *auth.Auth, admin config.AdminConfig, rt *router.Router, recorder *usage.Recorder, sqliteStore *store.SQLiteStore, providerNames []string) http.Handler {
	return NewWithClient(authenticator, admin, provider.NewClient(), rt, recorder, sqliteStore, providerNames)
}

func NewWithClient(authenticator *auth.Auth, admin config.AdminConfig, client provider.Client, rt *router.Router, recorder *usage.Recorder, sqliteStore *store.SQLiteStore, providerNames []string) http.Handler {
	providerNames = sortedStrings(providerNames)
	h := &Handler{
		auth:          authenticator,
		admin:         admin,
		client:        client,
		router:        rt,
		usage:         recorder,
		store:         sqliteStore,
		providerNames: providerNames,
		sessions:      newAdminSessionStore(),
		mux:           http.NewServeMux(),
	}
	h.executor = NewGatewayExecutor(rt, sqliteStore, client, recorder)

	h.routes()
	return h
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func (h *Handler) listProviderNames() []string {
	h.providerMu.RLock()
	defer h.providerMu.RUnlock()
	return append([]string(nil), h.providerNames...)
}

func (h *Handler) setProviderNames(names []string) {
	h.providerMu.Lock()
	h.providerNames = sortedStrings(names)
	h.providerMu.Unlock()
}

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /healthz", h.handleHealth)
	h.mux.HandleFunc("GET /assets/logo.svg", h.handleAdminLogo)
	h.mux.HandleFunc("GET /assets/avatar.svg", h.handleAdminAvatar)
	h.mux.HandleFunc("GET /favicon.svg", h.handleAdminFavicon)
	h.mux.HandleFunc("GET /v1/models", h.handleModels)
	h.mux.HandleFunc("GET /v1/models/{model...}", h.handleModel)
	h.mux.HandleFunc("POST /v1/chat/completions", h.handleChatCompletions)
	h.mux.HandleFunc("POST /anthropic/v1/messages", h.handleMessages)
	h.mux.HandleFunc("POST /v1/embeddings", h.handleEmbeddings)
	h.mux.HandleFunc("POST /v1/responses", h.handleResponses)
	h.mux.HandleFunc("GET /v1/usage", h.handleUsage)
	h.mux.HandleFunc("GET /admin/usage", h.handleAdminUsage)
	h.mux.HandleFunc("GET /admin", h.handleAdminHome)
	h.mux.HandleFunc("GET /", h.handleAdminHome)
	h.mux.HandleFunc("GET /admin/login", h.handleAdminLoginPage)
	h.mux.HandleFunc("POST /admin/login", h.handleAdminLogin)
	h.mux.HandleFunc("POST /admin/logout", h.handleAdminLogout)
	h.mux.HandleFunc("GET /admin/providers", h.handleAdminProvidersPage)
	h.mux.HandleFunc("POST /admin/providers", h.handleAdminProvidersSave)
	h.mux.HandleFunc("POST /admin/providers/delete", h.handleAdminProvidersDelete)
	h.mux.HandleFunc("GET /admin/keys", h.handleAdminKeysPage)
	h.mux.HandleFunc("POST /admin/keys", h.handleAdminKeysSave)
	h.mux.HandleFunc("POST /admin/keys/delete", h.handleAdminKeysDelete)
	h.mux.HandleFunc("GET /admin/models", h.handleAdminModelsPage)
	h.mux.HandleFunc("POST /admin/models", h.handleAdminModelsSave)
	h.mux.HandleFunc("POST /admin/models/delete", h.handleAdminModelsDelete)
	h.mux.HandleFunc("GET /admin/playground", h.handleAdminPlaygroundPage)
	h.mux.HandleFunc("GET /admin/usage/view", h.handleAdminUsagePage)
	h.mux.HandleFunc("GET /admin/usage/trend", h.handleAdminUsageTrend)

	// API admin routes
	h.mux.HandleFunc("GET /api/admin/providers", h.handleApiProvidersList)
	h.mux.HandleFunc("GET /api/admin/providers/{name}", h.handleApiProviderGet)
	h.mux.HandleFunc("POST /api/admin/providers", h.handleApiProvidersCreate)
	h.mux.HandleFunc("PUT /api/admin/providers/{name}", h.handleApiProviderUpdate)
	h.mux.HandleFunc("DELETE /api/admin/providers/{name}", h.handleApiProviderDelete)
	h.mux.HandleFunc("GET /api/admin/models", h.handleApiModelsList)
	h.mux.HandleFunc("POST /api/admin/models", h.handleApiModelsCreate)
	h.mux.HandleFunc("PUT /api/admin/models/{public_name}", h.handleApiModelUpdate)
	h.mux.HandleFunc("DELETE /api/admin/models/{public_name}", h.handleApiModelsDelete)
	h.mux.HandleFunc("GET /api/admin/keys", h.handleApiKeysList)
	h.mux.HandleFunc("POST /api/admin/keys", h.handleApiKeysCreate)
	h.mux.HandleFunc("PUT /api/admin/keys/{key}", h.handleApiKeyUpdate)
	h.mux.HandleFunc("DELETE /api/admin/keys/{key}", h.handleApiKeysDelete)
	h.mux.HandleFunc("GET /api/admin/routing", h.handleApiRoutingSettingsGet)
	h.mux.HandleFunc("PUT /api/admin/routing", h.handleApiRoutingSettingsUpdate)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleAdminLogo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(adminLogoSVG)
}

func (h *Handler) handleAdminFavicon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(adminFaviconSVG)
}

func (h *Handler) handleAdminAvatar(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(adminAvatarSVG)
}

func (h *Handler) ReloadModelsFromStore(ctx context.Context) error {
	return h.reloadModels(ctx)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, invalidMessage string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", invalidMessage)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", invalidMessage)
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return v
	}
	return r.RemoteAddr
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{
		Error: apiError{
			Message: message,
			Type:    "invalid_request_error",
			Code:    code,
		},
	})
}
