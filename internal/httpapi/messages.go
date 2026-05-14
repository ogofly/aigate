package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"aigate/internal/logger"
	"aigate/internal/provider"
	"aigate/internal/usage"
)

func (h *Handler) handleMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logger.L.Info("request", "op", "messages", "method", r.Method, "path", r.URL.Path)
	principal, ok := h.auth.Authenticate(r)
	if !ok {
		logger.L.Warn("auth failed", "op", "messages")
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

	plan, err := h.resolveRoutePlan(r, principal, req.Model, req.Raw)
	if err != nil {
		logger.L.Warn("model not found", "op", "messages", "model", req.Model)
		h.recordUsage(principal, "messages", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeRouteError(w, http.StatusBadRequest, err)
		return
	}
	target := plan[0]

	if req.Stream {
		requestID := nextStreamRequestID()
		var streamResp *provider.StreamResponse
		var lastErr error
		attemptLimit := maxAttempts(len(plan), h.routeAttempts(r.Context()))
		for attempt := 0; attempt < attemptLimit; attempt++ {
			target = plan[attempt]
			providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
			if err != nil {
				lastErr = providerNotFoundError(target, err)
				break
			}
			streamResp, err = h.client.MessagesStream(r.Context(), providerCfg, &req, target.UpstreamModel)
			if err != nil {
				lastErr = err
				if attempt+1 < attemptLimit && retryableUpstreamError(err) {
					continue
				}
				logger.L.Error("stream request failed", "op", "messages", "request_id", requestID, "model", req.Model, "provider", target.ProviderName, "client_ip", clientIP(r), "error", err)
				h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
				writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
				return
			}
			if streamResp.StatusCode >= 200 && streamResp.StatusCode < 300 {
				break
			}
			if attempt+1 < attemptLimit && retryableStatus(streamResp.StatusCode) {
				_, _ = io.Copy(io.Discard, streamResp.Body)
				streamResp.Body.Close()
				continue
			}
			break
		}
		if streamResp == nil {
			h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			writeError(w, http.StatusBadGateway, "upstream_error", lastErr.Error())
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
			logger.L.Error("stream request failed", "op", "messages", "request_id", requestID, "model", req.Model, "provider", target.ProviderName, "client_ip", clientIP(r), "upstream_status", streamResp.StatusCode, "client_ip", clientIP(r), "duration_ms", time.Since(start).Milliseconds())
			bytesSent, copyErr := io.Copy(w, streamResp.Body)
			if copyErr != nil {
				logger.L.Error("stream abort", "op", "messages", "request_id", requestID, "provider", target.ProviderName, "model", req.Model, "reason", "downstream_write_error", "error", copyErr, "client_ip", clientIP(r), "duration_ms", time.Since(start).Milliseconds(), "bytes_sent", bytesSent, "upstream_status", streamResp.StatusCode)
				return
			}
			h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, streamResp.StatusCode, time.Since(start))
			return
		}

		streamUsage, _, _, _, _, _, streamErr := proxyStreamBody(w, flusher, streamResp.Body, requestID, target.ProviderName, req.Model, start)
		if streamErr != nil {
			return
		}
		h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, true, streamUsage.requestTokens, streamUsage.responseTokens, streamUsage.totalTokens, streamResp.StatusCode, time.Since(start))
		return
	}

	var resp *provider.AnthropicResponse
	var lastErr error
	attemptLimit := maxAttempts(len(plan), h.routeAttempts(r.Context()))
	for attempt := 0; attempt < attemptLimit; attempt++ {
		target = plan[attempt]
		providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
		if err != nil {
			lastErr = providerNotFoundError(target, err)
			break
		}
		resp, err = h.client.Messages(r.Context(), providerCfg, &req, target.UpstreamModel)
		if err == nil {
			break
		}
		lastErr = err
		if attempt+1 < attemptLimit && retryableUpstreamError(err) {
			continue
		}
		logger.L.Error("messages request failed", "op", "messages", "model", req.Model, "provider", target.ProviderName, "client_ip", clientIP(r), "error", err)
		h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	if resp == nil {
		h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "upstream_error", lastErr.Error())
		return
	}
	requestTokens, responseTokens, totalTokens := usage.ExtractUsage(map[string]any(*resp))
	h.recordUsage(principal, "messages", target.ProviderName, req.Model, target.UpstreamModel, true, requestTokens, responseTokens, totalTokens, http.StatusOK, time.Since(start))
	writeJSON(w, http.StatusOK, resp)
	logger.L.Info("messages complete", "op", "messages", "model", req.Model, "provider", target.ProviderName, "client_ip", clientIP(r), "status", http.StatusOK, "request_tokens", requestTokens, "response_tokens", responseTokens, "total_tokens", totalTokens, "duration_ms", time.Since(start).Milliseconds())
}
