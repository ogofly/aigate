package httpapi

import (
	"aigate/internal/auth"
	"aigate/internal/logger"
	"aigate/internal/store"
	"aigate/internal/usage"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) handleUsage(w http.ResponseWriter, r *http.Request) {
	logger.L.Info("request", "op", "usage", "query", r.URL.RawQuery)
	principal, ok := h.auth.Authenticate(r)
	if !ok {
		logger.L.Warn("auth failed", "op", "usage")
		writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid api key")
		return
	}

	view := usageView(r)
	if view != "" && view != "summary" {
		h.handleUsageQuery(w, r, principal, view)
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

func usageView(r *http.Request) string {
	q := r.URL.Query()
	if view := strings.TrimSpace(q.Get("view")); view != "" {
		return view
	}
	if strings.TrimSpace(q.Get("group_by")) != "" || strings.TrimSpace(q.Get("groupBy")) != "" {
		return "trend"
	}
	if strings.TrimSpace(q.Get("start")) != "" || strings.TrimSpace(q.Get("end")) != "" || strings.TrimSpace(q.Get("model")) != "" {
		return "by_model"
	}
	return ""
}

func (h *Handler) handleUsageQuery(w http.ResponseWriter, r *http.Request, principal auth.Principal, view string) {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "usage_query_unavailable", "usage query store unavailable")
		return
	}

	filter, groupBy, err := parseUsageQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_usage_query", err.Error())
		return
	}
	filter.KeyID = usage.KeyID(principal.Key)

	switch view {
	case "by_model":
		models, err := h.store.QueryUsageByModel(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "usage_query_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": models})
	case "trend":
		points, err := h.store.QueryUsageTrend(r.Context(), filter, groupBy)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "usage_query_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": points})
	default:
		writeError(w, http.StatusBadRequest, "invalid_usage_view", "unsupported usage view")
		return
	}

	logger.L.Info("response", "op", "usage_query", "key", principal.Key, "view", view, "status", http.StatusOK)
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

func parseUsageQuery(r *http.Request) (store.UsageFilter, string, error) {
	q := r.URL.Query()
	filter := store.UsageFilter{}

	if startStr := strings.TrimSpace(q.Get("start")); startStr != "" {
		t, err := time.ParseInLocation("2006-01-02", startStr, time.Local)
		if err != nil {
			return store.UsageFilter{}, "", fmt.Errorf("invalid start date %q", startStr)
		}
		filter.StartTime = t
	}
	if endStr := strings.TrimSpace(q.Get("end")); endStr != "" {
		t, err := time.ParseInLocation("2006-01-02", endStr, time.Local)
		if err != nil {
			return store.UsageFilter{}, "", fmt.Errorf("invalid end date %q", endStr)
		}
		// Store queries already treat EndTime as an inclusive end date.
		filter.EndTime = t
	}
	if model := strings.TrimSpace(q.Get("model")); model != "" {
		filter.Model = model
	}

	groupBy := strings.TrimSpace(q.Get("group_by"))
	if groupBy == "" {
		groupBy = strings.TrimSpace(q.Get("groupBy"))
	}
	if groupBy == "" {
		groupBy = "day"
	}
	if groupBy != "day" && groupBy != "hour" {
		return store.UsageFilter{}, "", fmt.Errorf("invalid group_by %q", groupBy)
	}

	return filter, groupBy, nil
}
