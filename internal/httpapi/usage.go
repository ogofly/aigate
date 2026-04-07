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
		"data": h.decorateSummary(summary, principal),
	})
	log.Printf("method=%s path=%s op=usage key=%s status=%d", r.Method, r.URL.Path, principal.Key, http.StatusOK)
}

func (h *Handler) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	log.Printf("method=%s path=%s op=admin_usage", r.Method, r.URL.Path)
	session, ok := h.webSession(r)
	if !ok {
		log.Printf("method=%s path=%s op=admin_usage session=failed", r.Method, r.URL.Path)
		writeError(w, http.StatusUnauthorized, "web_auth_required", "web session required")
		return
	}
	summaries := h.usage.Summaries()
	if session.Role != roleAdmin {
		summaries = filterUsageByOwner(summaries, session.Username)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": summaries,
	})
	log.Printf("method=%s path=%s op=admin_usage status=%d", r.Method, r.URL.Path, http.StatusOK)
}

func (h *Handler) emptySummary(principal auth.Principal) usage.Summary {
	return usage.Summary{
		KeyID:   usage.KeyID(principal.Key),
		APIKey:  principal.Key,
		KeyName: principal.Name,
		Owner:   principal.Owner,
		Purpose: principal.Purpose,
	}
}

func (h *Handler) decorateSummary(summary usage.Summary, principal auth.Principal) usage.Summary {
	summary.KeyID = usage.KeyID(principal.Key)
	summary.APIKey = principal.Key
	if summary.KeyName == "" {
		summary.KeyName = principal.Name
	}
	if summary.Owner == "" {
		summary.Owner = principal.Owner
	}
	if summary.Purpose == "" {
		summary.Purpose = principal.Purpose
	}
	return summary
}
