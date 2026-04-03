package httpapi

import (
	"log"
	"net/http"

	"aigate/internal/auth"
	"aigate/internal/usage"
)

func (h *Handler) handleUsage(w http.ResponseWriter, r *http.Request) {
	log.Printf("method=%s path=%s op=usage", r.Method, r.URL.Path)
	principal, ok := h.auth.Authenticate(r)
	if !ok {
		log.Printf("method=%s path=%s op=usage auth=failed", r.Method, r.URL.Path)
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	summary, ok := h.usage.SummaryByKey(principal.Key)
	if !ok {
		summary = h.emptySummary(principal)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": summary,
	})
	log.Printf("method=%s path=%s op=usage key=%s status=%d", r.Method, r.URL.Path, principal.Key, http.StatusOK)
}

func (h *Handler) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	log.Printf("method=%s path=%s op=admin_usage", r.Method, r.URL.Path)
	principal, ok := h.auth.Authenticate(r)
	if !ok {
		log.Printf("method=%s path=%s op=admin_usage auth=failed", r.Method, r.URL.Path)
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}
	if !principal.Admin {
		log.Printf("method=%s path=%s op=admin_usage key=%s forbidden=true", r.Method, r.URL.Path, principal.Key)
		writeError(w, http.StatusForbidden, "forbidden", "admin access required")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": h.usage.Summaries(),
	})
	log.Printf("method=%s path=%s op=admin_usage key=%s status=%d", r.Method, r.URL.Path, principal.Key, http.StatusOK)
}

func (h *Handler) emptySummary(principal auth.Principal) usage.Summary {
	return usage.Summary{
		APIKey:  principal.Key,
		KeyName: principal.Name,
		Owner:   principal.Owner,
		Purpose: principal.Purpose,
	}
}
