package httpapi

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"aigate/internal/provider"
)

func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	log.Printf("method=%s path=%s op=chat_completions", r.Method, r.URL.Path)
	if !h.auth.Check(r) {
		log.Printf("method=%s path=%s op=chat_completions auth=failed", r.Method, r.URL.Path)
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	if req.Model == "" {
		log.Printf("method=%s path=%s op=chat_completions error=model_required", r.Method, r.URL.Path)
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	target, err := h.router.Resolve(req.Model)
	if err != nil {
		log.Printf("method=%s path=%s op=chat_completions model=%s error=model_not_found", r.Method, r.URL.Path, req.Model)
		writeError(w, http.StatusBadRequest, "model_not_found", "model not found")
		return
	}
	log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s upstream_model=%s stream=%t", r.Method, r.URL.Path, req.Model, target.ProviderName, target.UpstreamModel, req.Stream)

	if req.Stream {
		stream, err := target.Provider.ChatStream(r.Context(), &req, target.UpstreamModel)
		if err != nil {
			log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s error=%v", r.Method, r.URL.Path, req.Model, target.ProviderName, err)
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		defer stream.Close()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "stream_unsupported", "streaming unsupported")
			return
		}

		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, stream); err != nil {
			log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s stream_copy_error=%v", r.Method, r.URL.Path, req.Model, target.ProviderName, err)
			return
		}
		flusher.Flush()
		log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s status=%d stream=%t", r.Method, r.URL.Path, req.Model, target.ProviderName, http.StatusOK, true)
		return
	}

	resp, err := target.Provider.Chat(r.Context(), &req, target.UpstreamModel)
	if err != nil {
		log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s error=%v", r.Method, r.URL.Path, req.Model, target.ProviderName, err)
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, resp)
	log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s status=%d stream=%t", r.Method, r.URL.Path, req.Model, target.ProviderName, http.StatusOK, false)
}
