package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"aigate/internal/logger"
	"aigate/internal/provider"
	"aigate/internal/usage"
)

func (h *Handler) handleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logger.L.Info("request", "op", "responses", "method", r.Method, "path", r.URL.Path)
	principal, ok := h.auth.Authenticate(r)
	if !ok {
		logger.L.Warn("auth failed", "op", "responses", "client_ip", clientIP(r))
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	var req provider.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	if req.Model == "" {
		logger.L.Warn("model required", "op", "responses", "client_ip", clientIP(r))
		h.recordUsage(principal, "responses", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	target, err := h.router.Resolve(req.Model)
	if err != nil {
		logger.L.Warn("model not found", "op", "responses", "model", req.Model, "client_ip", clientIP(r))
		h.recordUsage(principal, "responses", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_not_found", "model not found")
		return
	}
	logger.L.Info("request resolved", "op", "responses", "model", req.Model, "provider", target.ProviderName, "upstream_model", target.UpstreamModel, "client_ip", clientIP(r))

	providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
	if err != nil {
		h.recordUsage(principal, "responses", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "provider_not_found", err.Error())
		return
	}

	resp, err := h.client.Responses(r.Context(), providerCfg, &req, target.UpstreamModel)
	if err != nil {
		logger.L.Error("responses request failed", "op", "responses", "model", req.Model, "provider", target.ProviderName, "client_ip", clientIP(r), "error", err)
		h.recordUsage(principal, "responses", target.ProviderName, req.Model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	requestTokens, responseTokens, totalTokens := usage.ExtractUsage(map[string]any(*resp))
	h.recordUsage(principal, "responses", target.ProviderName, req.Model, target.UpstreamModel, true, requestTokens, responseTokens, totalTokens, http.StatusOK, time.Since(start))
	writeJSON(w, http.StatusOK, resp)
	logger.L.Info("response complete", "op", "responses", "model", req.Model, "provider", target.ProviderName, "client_ip", clientIP(r), "status", http.StatusOK, "request_tokens", requestTokens, "response_tokens", responseTokens, "total_tokens", totalTokens, "duration_ms", time.Since(start).Milliseconds())
}
