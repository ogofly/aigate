package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/provider"
	"aigate/internal/router"
	"aigate/internal/store"
	"aigate/internal/usage"
)

type Handler struct {
	auth          *auth.Auth
	admin         config.AdminConfig
	client        provider.Client
	router        *router.Router
	usage         *usage.Recorder
	store         *store.SQLiteStore
	providerNames []string
	sessions      *adminSessionStore
	mux           *http.ServeMux
}

func New(authenticator *auth.Auth, admin config.AdminConfig, rt *router.Router, recorder *usage.Recorder, sqliteStore *store.SQLiteStore, providerNames []string) http.Handler {
	return NewWithClient(authenticator, admin, provider.NewClient(), rt, recorder, sqliteStore, providerNames)
}

func NewWithClient(authenticator *auth.Auth, admin config.AdminConfig, client provider.Client, rt *router.Router, recorder *usage.Recorder, sqliteStore *store.SQLiteStore, providerNames []string) http.Handler {
	sort.Strings(providerNames)
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

	h.routes()
	return h
}

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /healthz", h.handleHealth)
	h.mux.HandleFunc("GET /v1/models", h.handleModels)
	h.mux.HandleFunc("POST /v1/chat/completions", h.handleChatCompletions)
	h.mux.HandleFunc("POST /anthropic/v1/messages", h.handleMessages)
	h.mux.HandleFunc("POST /v1/embeddings", h.handleEmbeddings)
	h.mux.HandleFunc("GET /v1/usage", h.handleUsage)
	h.mux.HandleFunc("GET /admin/usage", h.handleAdminUsage)
	h.mux.HandleFunc("GET /admin", h.handleAdminHome)
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
	h.mux.HandleFunc("POST /admin/playground/chat", h.handleAdminPlaygroundChat)
	h.mux.HandleFunc("POST /admin/playground/chat/ajax", h.handleAdminPlaygroundChatAJAX)
	h.mux.HandleFunc("GET /admin/usage/view", h.handleAdminUsagePage)
	h.mux.HandleFunc("GET /admin/usage/trend", h.handleAdminUsageTrend)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) ReloadModelsFromStore(ctx context.Context) error {
	return h.reloadModels(ctx)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
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
