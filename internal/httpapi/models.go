package httpapi

import (
	"log"
	"net/http"
)

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	log.Printf("method=%s path=%s op=models", r.Method, r.URL.Path)
	if !h.auth.Check(r) {
		log.Printf("method=%s path=%s op=models auth=failed", r.Method, r.URL.Path)
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
	log.Printf("method=%s path=%s op=models status=%d count=%d", r.Method, r.URL.Path, http.StatusOK, len(data))
}
