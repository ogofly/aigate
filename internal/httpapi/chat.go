package httpapi

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"aigate/internal/auth"
	"aigate/internal/provider"
	"aigate/internal/usage"
)

func (h *Handler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	log.Printf("method=%s path=%s op=chat_completions", r.Method, r.URL.Path)
	principal, ok := h.auth.Authenticate(r)
	if !ok {
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
		h.recordUsage(principal, "chat.completions", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	target, err := h.router.Resolve(req.Model)
	if err != nil {
		log.Printf("method=%s path=%s op=chat_completions model=%s error=model_not_found", r.Method, r.URL.Path, req.Model)
		h.recordUsage(principal, "chat.completions", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_not_found", "model not found")
		return
	}
	log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s upstream_model=%s stream=%t", r.Method, r.URL.Path, req.Model, target.ProviderName, target.UpstreamModel, req.Stream)
	providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
	if err != nil {
		h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "provider_not_found", err.Error())
		return
	}

	if req.Stream {
		stream, err := h.client.ChatStream(r.Context(), providerCfg, &req, target.UpstreamModel)
		if err != nil {
			log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s error=%v", r.Method, r.URL.Path, req.Model, target.ProviderName, err)
			h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
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
		h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, true, 0, 0, 0, http.StatusOK, time.Since(start))
		log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s status=%d stream=%t", r.Method, r.URL.Path, req.Model, target.ProviderName, http.StatusOK, true)
		return
	}

	resp, err := h.client.Chat(r.Context(), providerCfg, &req, target.UpstreamModel)
	if err != nil {
		log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s error=%v", r.Method, r.URL.Path, req.Model, target.ProviderName, err)
		h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	requestTokens, responseTokens, totalTokens := usage.ExtractUsage(map[string]any(*resp))
	h.recordUsage(principal, "chat.completions", target.ProviderName, req.Model, target.UpstreamModel, true, requestTokens, responseTokens, totalTokens, http.StatusOK, time.Since(start))
	writeJSON(w, http.StatusOK, resp)
	log.Printf("method=%s path=%s op=chat_completions model=%s provider=%s status=%d stream=%t", r.Method, r.URL.Path, req.Model, target.ProviderName, http.StatusOK, false)
}

func (h *Handler) recordUsage(principal auth.Principal, endpoint, providerName, publicModel, upstreamModel string, success bool, requestTokens, responseTokens, totalTokens, statusCode int, latency time.Duration) {
	if h.usage == nil {
		return
	}
	h.usage.Record(usage.NewRecord(principal, endpoint, providerName, publicModel, upstreamModel, success, requestTokens, responseTokens, totalTokens, statusCode, latency))
}
