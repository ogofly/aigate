package httpapi

import (
	"aigate/internal/auth"
	"aigate/internal/logger"
	"aigate/internal/usage"
	"net/http"
)

func (h *Handler) handleUsage(w http.ResponseWriter, r *http.Request) {
	logger.L.Info("request", "op", "usage")
	principal, ok := h.auth.Authenticate(r)
	if !ok {
		logger.L.Warn("auth failed", "op", "usage")
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
	logger.L.Info("response", "op", "usage", "key", principal.Key, "status", http.StatusOK)
}

func (h *Handler) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	logger.L.Info("request", "op", "admin_usage")
	session, ok := h.webSession(r)
	if !ok {
		logger.L.Warn("session failed", "op", "admin_usage")
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
	logger.L.Info("response", "op", "admin_usage", "status", http.StatusOK)
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
