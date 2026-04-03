package httpapi

import (
	"encoding/json"
	"log"
	"net/http"

	"aigate/internal/provider"
)

func (h *Handler) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	log.Printf("method=%s path=%s op=embeddings", r.Method, r.URL.Path)
	if !h.auth.Check(r) {
		log.Printf("method=%s path=%s op=embeddings auth=failed", r.Method, r.URL.Path)
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	var req provider.EmbeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	modelValue, ok := req["model"]
	model, ok2 := modelValue.(string)
	if !ok || !ok2 || model == "" {
		log.Printf("method=%s path=%s op=embeddings error=model_required", r.Method, r.URL.Path)
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	target, err := h.router.Resolve(model)
	if err != nil {
		log.Printf("method=%s path=%s op=embeddings model=%s error=model_not_found", r.Method, r.URL.Path, model)
		writeError(w, http.StatusBadRequest, "model_not_found", "model not found")
		return
	}
	log.Printf("method=%s path=%s op=embeddings model=%s provider=%s upstream_model=%s", r.Method, r.URL.Path, model, target.ProviderName, target.UpstreamModel)

	resp, err := target.Provider.Embed(r.Context(), req, target.UpstreamModel)
	if err != nil {
		log.Printf("method=%s path=%s op=embeddings model=%s provider=%s error=%v", r.Method, r.URL.Path, model, target.ProviderName, err)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
	log.Printf("method=%s path=%s op=embeddings model=%s provider=%s status=%d", r.Method, r.URL.Path, model, target.ProviderName, http.StatusOK)
}
