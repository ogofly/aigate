package httpapi

import (
	"context"
	"net/http"
	"time"

	"llmgate/internal/config"
	"llmgate/internal/logger"
	"llmgate/internal/provider"
	"llmgate/internal/router"
)

func (h *Handler) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logger.L.Info("request", "op", "embeddings", "method", r.Method, "path", r.URL.Path)
	principal, ok := h.auth.Authenticate(r)
	if !ok {
		logger.L.Warn("auth failed", "op", "embeddings")
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	var req provider.EmbeddingRequest
	if !decodeJSONBody(w, r, &req, "invalid request body") {
		return
	}

	modelValue, ok := req["model"]
	model, ok2 := modelValue.(string)
	if !ok || !ok2 || model == "" {
		logger.L.Warn("model required", "op", "embeddings")
		h.executor.recordUsage(principal, "embeddings", "", "", "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	result, errResp := h.executor.ExecuteJSON(r.Context(), r, principal, start, gatewayJSONEndpoint{
		op:            "embeddings",
		usageEndpoint: "embeddings",
		model:         model,
		raw:           req,
		call: func(ctx context.Context, client gatewayProviderClient, providerCfg config.ProviderConfig, target router.RouteTarget) (any, error) {
			return client.Embed(ctx, providerCfg, req, target.UpstreamModel)
		},
	})
	if errResp != nil {
		writeGatewayError(w, errResp)
		return
	}
	writeJSON(w, result.status, result.payload)
}
