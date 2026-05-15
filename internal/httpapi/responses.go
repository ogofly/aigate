package httpapi

import (
	"context"
	"net/http"
	"time"

	"llmgate/internal/config"
	"llmgate/internal/logger"
	"llmgate/internal/provider"
	"llmgate/internal/router"
	"llmgate/internal/usage"
)

// OpenAI-like response API
func (h *Handler) handleResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logger.L.Info("request", "op", "responses", "method", r.Method, "path", r.URL.Path, "client_ip", clientIP(r), "content_type", r.Header.Get("Content-Type"))

	principal, ok := h.auth.Authenticate(r)
	if !ok {
		logger.L.Warn("auth failed", "op", "responses", "client_ip", clientIP(r))
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}
	logger.L.Info("auth ok", "op", "responses", "key_id", usage.KeyID(principal.Key), "client_ip", clientIP(r))

	var req provider.ChatRequest
	if !decodeJSONBody(w, r, &req, "invalid request body") {
		logger.L.Warn("decode body failed", "op", "responses", "client_ip", clientIP(r))
		return
	}
	logger.L.Info("body decoded", "op", "responses", "model", req.Model, "client_ip", clientIP(r))

	if req.Model == "" {
		logger.L.Warn("model required", "op", "responses", "client_ip", clientIP(r))
		h.executor.recordUsage(principal, "responses", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	if req.Stream {
		h.executor.ExecuteStream(w, r, principal, start, gatewayStreamEndpoint{
			op:            "responses",
			usageEndpoint: "responses",
			model:         req.Model,
			raw:           req.Raw,
			call: func(ctx context.Context, client gatewayProviderClient, providerCfg config.ProviderConfig, target router.RouteTarget) (*provider.StreamResponse, error) {
				return client.ResponsesStream(ctx, providerCfg, &req, target.UpstreamModel)
			},
		})
		return
	}

	result, errResp := h.executor.ExecuteJSON(r.Context(), r, principal, start, gatewayJSONEndpoint{
		op:            "responses",
		usageEndpoint: "responses",
		model:         req.Model,
		raw:           req.Raw,
		call: func(ctx context.Context, client gatewayProviderClient, providerCfg config.ProviderConfig, target router.RouteTarget) (any, error) {
			return client.Responses(ctx, providerCfg, &req, target.UpstreamModel)
		},
	})
	if errResp != nil {
		writeGatewayError(w, errResp)
		return
	}
	writeJSON(w, result.status, result.payload)
}
