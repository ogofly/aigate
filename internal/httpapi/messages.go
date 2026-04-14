package httpapi

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"aigate/internal/provider"
	"aigate/internal/usage"
)

func (h *Handler) handleMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	log.Printf("method=%s path=%s op=messages", r.Method, r.URL.Path)
	principal, ok := h.auth.Authenticate(r)
	if !ok {
		log.Printf("method=%s path=%s op=messages auth=failed", r.Method, r.URL.Path)
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if req.Model == "" {
		h.recordUsage(principal, "messages", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	target, err := h.router.Resolve(req.Model)
	if err != nil {
		log.Printf("method=%s path=%s op=messages model=%s error=model_not_found", r.Method, r.URL.Path, req.Model)
		h.recordUsage(principal, "messages", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_not_found", "model not found")
		return
	}
	providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
	if err != nil {
		h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "provider_not_found", err.Error())
		return
	}

	if req.Stream {
		requestID := nextStreamRequestID()
		streamResp, err := h.client.MessagesStream(r.Context(), providerCfg, &req, target.UpstreamModel)
		if err != nil {
			log.Printf("method=%s path=%s op=messages request_id=%s model=%s provider=%s error=%v", r.Method, r.URL.Path, requestID, req.Model, target.ProviderName, err)
			h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
			return
		}
		defer streamResp.Body.Close()

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "stream_unsupported", "streaming unsupported")
			return
		}

		copyProxyHeaders(w.Header(), streamResp.Header)
		w.WriteHeader(streamResp.StatusCode)
		if streamResp.StatusCode < 200 || streamResp.StatusCode >= 300 {
			bytesSent, copyErr := io.Copy(w, streamResp.Body)
			if copyErr != nil {
				log.Printf("method=%s path=%s op=messages event=stream_abort request_id=%s provider=%s model=%s reason=downstream_write_error err=%v duration_ms=%d bytes_sent=%d upstream_status=%d", r.Method, r.URL.Path, requestID, target.ProviderName, req.Model, copyErr, time.Since(start).Milliseconds(), bytesSent, streamResp.StatusCode)
				return
			}
			h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, streamResp.StatusCode, time.Since(start))
			return
		}

		streamUsage, _, _, _, _, _, streamErr := proxyStreamBody(w, flusher, streamResp.Body, r.Method, r.URL.Path, requestID, target.ProviderName, req.Model, start)
		if streamErr != nil {
			return
		}
		h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, true, streamUsage.requestTokens, streamUsage.responseTokens, streamUsage.totalTokens, streamResp.StatusCode, time.Since(start))
		return
	}

	resp, err := h.client.Messages(r.Context(), providerCfg, &req, target.UpstreamModel)
	if err != nil {
		log.Printf("method=%s path=%s op=messages model=%s provider=%s error=%v", r.Method, r.URL.Path, req.Model, target.ProviderName, err)
		h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	requestTokens, responseTokens, totalTokens := usage.ExtractUsage(map[string]any(*resp))
	h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, true, requestTokens, responseTokens, totalTokens, http.StatusOK, time.Since(start))
	writeJSON(w, http.StatusOK, resp)
	log.Printf("method=%s path=%s op=messages model=%s provider=%s status=%d stream=%t", r.Method, r.URL.Path, req.Model, target.ProviderName, http.StatusOK, false)
}
