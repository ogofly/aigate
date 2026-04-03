package httpapi

import (
	"encoding/json"
	"net/http"

	"aigate/internal/auth"
	"aigate/internal/router"
	"aigate/internal/usage"
)

type Handler struct {
	auth   *auth.Auth
	router *router.Router
	usage  *usage.Recorder
	mux    *http.ServeMux
}

func New(authenticator *auth.Auth, rt *router.Router, recorder *usage.Recorder) http.Handler {
	h := &Handler{
		auth:   authenticator,
		router: rt,
		usage:  recorder,
		mux:    http.NewServeMux(),
	}

	h.routes()
	return h
}

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /healthz", h.handleHealth)
	h.mux.HandleFunc("GET /v1/models", h.handleModels)
	h.mux.HandleFunc("POST /v1/chat/completions", h.handleChatCompletions)
	h.mux.HandleFunc("POST /v1/embeddings", h.handleEmbeddings)
	h.mux.HandleFunc("GET /v1/usage", h.handleUsage)
	h.mux.HandleFunc("GET /admin/usage", h.handleAdminUsage)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
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
