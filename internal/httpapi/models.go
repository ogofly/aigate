package httpapi

import (
	"aigate/internal/logger"
	"net/http"
)

type modelResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	logger.L.Info("request", "op", "models")
	if !h.auth.Check(r) {
		logger.L.Warn("auth failed", "op", "models")
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	models := h.router.ListModels()
	data := make([]modelResponse, 0, len(models))
	for _, model := range models {
		data = append(data, buildModelResponse(model))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
	logger.L.Info("response", "op", "models", "status", http.StatusOK, "count", len(data))
}

func (h *Handler) handleModel(w http.ResponseWriter, r *http.Request) {
	logger.L.Info("request", "op", "model_detail")
	if !h.auth.Check(r) {
		logger.L.Warn("auth failed", "op", "model_detail")
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	model := r.PathValue("model")
	if model == "" {
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}
	if _, err := h.router.Resolve(model); err != nil {
		writeError(w, http.StatusNotFound, "model_not_found", "model not found")
		return
	}

	writeJSON(w, http.StatusOK, buildModelResponse(model))
	logger.L.Info("response", "op", "model_detail", "status", http.StatusOK, "model", model)
}

func buildModelResponse(model string) modelResponse {
	return modelResponse{
		ID:      model,
		Object:  "model",
		Created: 0,
		OwnedBy: "aigate",
	}
}
