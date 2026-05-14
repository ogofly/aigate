package httpapi

import (
	"context"
	"net/http"
	"time"

	"aigate/internal/config"
	"aigate/internal/logger"
	"aigate/internal/provider"
	"aigate/internal/router"
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
	if !decodeJSONBody(w, r, &req, "invalid request body") {
		return
	}
	if req.Model == "" {
		h.executor.recordUsage(principal, "messages", "", req.Model, "", false, 0, 0, 0, http.StatusBadRequest, time.Since(start))
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	if req.Stream {
		h.executor.ExecuteStream(w, r, principal, start, gatewayStreamEndpoint{
			op:            "messages",
			usageEndpoint: "messages",
			model:         req.Model,
			raw:           req.Raw,
			call: func(ctx context.Context, client gatewayProviderClient, providerCfg config.ProviderConfig, target router.RouteTarget) (*provider.StreamResponse, error) {
				return client.MessagesStream(ctx, providerCfg, &req, target.UpstreamModel)
			},
		})
		return
	}

	result, errResp := h.executor.ExecuteJSON(r.Context(), r, principal, start, gatewayJSONEndpoint{
		op:            "messages",
		usageEndpoint: "messages",
		model:         req.Model,
		raw:           req.Raw,
		call: func(ctx context.Context, client gatewayProviderClient, providerCfg config.ProviderConfig, target router.RouteTarget) (any, error) {
			return client.Messages(ctx, providerCfg, &req, target.UpstreamModel)
		},
	})
	if errResp != nil {
		writeGatewayError(w, errResp)
		return
	}
	writeJSON(w, result.status, result.payload)
}
