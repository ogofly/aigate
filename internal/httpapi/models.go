package httpapi

import (
	"aigate/internal/logger"
	"net/http"
)

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	logger.L.Info("request", "op", "models")
	if !h.auth.Check(r) {
		logger.L.Warn("auth failed", "op", "models")
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	models := h.router.ListModels()
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{
			"id":       model,
			"object":   "model",
			"owned_by": "aigate",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
	logger.L.Info("response", "op", "models", "status", http.StatusOK, "count", len(data))
}
