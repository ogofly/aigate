package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/logger"
	"aigate/internal/provider"
	"aigate/internal/store"
	"aigate/internal/usage"
)

const adminSessionCookie = "aigate_admin_session"
const adminSystemName = "LLM Gateway"
const adminSessionTTL = 24 * time.Hour

type webRole string

const (
	roleAdmin webRole = "admin"
	roleUser  webRole = "user"
)

type webSession struct {
	Role      webRole
	Username  string
	ExpiresAt time.Time
}

type adminSessionStore struct {
	mu     sync.RWMutex
	tokens map[string]webSession
}

func adminPageTitle(page string) string {
	if page == "" {
		return adminSystemName
	}
	return page + " · " + adminSystemName
}

func newAdminSessionStore() *adminSessionStore {
	return &adminSessionStore{tokens: make(map[string]webSession)}
}

func (s *adminSessionStore) New(session webSession) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	session.ExpiresAt = time.Now().Add(adminSessionTTL)
	s.mu.Lock()
	s.tokens[token] = session
	s.mu.Unlock()
	return token, nil
}

func (s *adminSessionStore) Get(token string) (webSession, bool) {
	s.mu.RLock()
	session, ok := s.tokens[token]
	s.mu.RUnlock()
	if !ok {
		return webSession{}, false
	}
	if time.Now().After(session.ExpiresAt) {
		s.Delete(token)
		return webSession{}, false
	}
	return session, true
}

func (s *adminSessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

type adminViewData struct {
	Title                string
	Error                string
	IsAdmin              bool
	CurrentUser          string
	Keys                 []config.KeyConfig
	ProvidersCfg         []config.ProviderConfig
	Providers            []string
	Models               []config.ModelConfig
	Usage                []usage.Summary
	CurrentPath          string
	Flash                string
	DefaultPublic        string
	PlayModels           []string
	PlayAPIBase          string
	PlayAPIKey           string
	PlayModel            string
	PlayMessage          string
	PlayStream           bool
	PlayUseAnthropic     bool
	PlayUseResponses     bool
	PlayResult           string
	PlayError            string
	APIBase              string
	UsageModels          []string
	UsageKeys            []adminUsageKeyOption
	UsageOwners          []string
	FilterStart          string
	FilterEnd            string
	FilterModel          string
	FilterKey            string
	FilterOwner          string
	View                 string
	ModelSummaries       []usage.ModelSummary
	HasAnthropicProvider bool
	GroupBy              string
	PieMetric            string
	TrendHourPoints      []store.TrendPoint
	TrendDayPoints       []store.TrendPoint
}

type adminUsageKeyOption struct {
	Value string
	Label string
}

func (h *Handler) handleAdminLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.webSession(r); ok {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	_ = adminLoginTemplate.Execute(w, adminViewData{Title: adminPageTitle("Login")})
}

func (h *Handler) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		_ = adminLoginTemplate.Execute(w, adminViewData{Title: adminPageTitle("Login"), Error: "invalid form"})
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := strings.TrimSpace(r.FormValue("password"))
	session := webSession{}
	switch {
	case username == h.admin.Username && password == h.admin.Password:
		session = webSession{Role: roleAdmin, Username: username}
	default:
		keyCfg, err := h.store.GetAuthKey(r.Context(), password)
		if err != nil || keyCfg.Owner == "" || keyCfg.Owner != username {
			_ = adminLoginTemplate.Execute(w, adminViewData{Title: adminPageTitle("Login"), Error: "invalid credentials"})
			return
		}
		session = webSession{Role: roleUser, Username: username}
	}

	token, err := h.sessions.New(session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_session_error", "failed to create session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(adminSessionTTL.Seconds()),
		Expires:  time.Now().Add(adminSessionTTL),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsSecure(r),
	})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *Handler) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		h.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsSecure(r),
	})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *Handler) handleAdminHome(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/usage/view", http.StatusSeeOther)
}

func (h *Handler) handleAdminKeysPage(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	keys, err := h.visibleKeys(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_keys_error", err.Error())
		return
	}
	data := adminViewData{
		Title:       adminPageTitle("Keys"),
		IsAdmin:     session.Role == roleAdmin,
		CurrentUser: session.Username,
		Keys:        keys,
		APIBase:     publicAPIBaseURL(r),
		CurrentPath: "/admin/keys",
		Flash:       r.URL.Query().Get("flash"),
	}
	_ = adminKeysTemplate.Execute(w, data)
}

func (h *Handler) handleAdminKeysSave(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	key := config.KeyConfig{
		Key:     strings.TrimSpace(r.FormValue("key")),
		Name:    strings.TrimSpace(r.FormValue("name")),
		Owner:   strings.TrimSpace(r.FormValue("owner")),
		Purpose: strings.TrimSpace(r.FormValue("purpose")),
	}
	if session.Role != roleAdmin {
		key.Owner = session.Username
	}
	if key.Key == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	if err := h.store.UpsertAuthKey(r.Context(), key); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_key_save_error", err.Error())
		return
	}
	if err := h.reloadAuthKeys(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_key_reload_error", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/keys?flash=key+saved", http.StatusSeeOther)
}

func (h *Handler) handleAdminKeysDelete(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	if session.Role != roleAdmin {
		keyCfg, err := h.store.GetAuthKey(r.Context(), key)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "key not found")
			return
		}
		if keyCfg.Owner != session.Username {
			writeError(w, http.StatusForbidden, "forbidden", "cannot delete key of another owner")
			return
		}
	}
	if err := h.store.DeleteAuthKey(r.Context(), key); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_key_delete_error", err.Error())
		return
	}
	if err := h.reloadAuthKeys(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_key_reload_error", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/keys?flash=key+deleted", http.StatusSeeOther)
}

func (h *Handler) handleAdminProvidersPage(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	if session.Role != roleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "admin required")
		return
	}
	providersCfg, err := h.store.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_providers_error", err.Error())
		return
	}
	data := adminViewData{
		Title:        adminPageTitle("Providers"),
		IsAdmin:      true,
		CurrentUser:  session.Username,
		ProvidersCfg: providersCfg,
		CurrentPath:  "/admin/providers",
		Flash:        r.URL.Query().Get("flash"),
	}
	_ = adminProvidersTemplate.Execute(w, data)
}

func (h *Handler) handleAdminProvidersSave(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}

	if r.FormValue("_method") == "PUT" {
		h.handleAdminProviderUpdate(w, r, name)
		return
	}

	timeoutSeconds := 60
	if v := strings.TrimSpace(r.FormValue("timeout")); v != "" {
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil && parsed > 0 {
			timeoutSeconds = parsed
		}
	}
	providerCfg := config.ProviderConfig{
		Name:             name,
		APIKey:           strings.TrimSpace(r.FormValue("api_key")),
		BaseURL:          strings.TrimSpace(r.FormValue("base_url")),
		AnthropicBaseURL: strings.TrimSpace(r.FormValue("anthropic_base_url")),
		AnthropicVersion: strings.TrimSpace(r.FormValue("anthropic_version")),
		APIKeyRef:        strings.TrimSpace(r.FormValue("api_key_ref")),
		TimeoutSeconds:   timeoutSeconds,
	}
	if err := providerCfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.store.UpsertProvider(r.Context(), providerCfg); err != nil {
		writeError(w, http.StatusBadRequest, "admin_provider_save_error", err.Error())
		return
	}
	if err := h.reloadProvidersAndModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_provider_reload_error", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/providers?flash=provider+saved", http.StatusSeeOther)
}

func (h *Handler) handleAdminProviderUpdate(w http.ResponseWriter, r *http.Request, name string) {
	existing, err := h.store.GetProvider(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "admin_provider_update_error", "provider not found")
		return
	}

	baseURL := strings.TrimSpace(r.FormValue("base_url"))
	if baseURL == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "base_url is required")
		return
	}
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	apiKeyRef := strings.TrimSpace(r.FormValue("api_key_ref"))
	if apiKey == "" && apiKeyRef == "" {
		apiKey = existing.APIKey
		apiKeyRef = existing.APIKeyRef
	}

	providerCfg := config.ProviderConfig{
		Name:             name,
		BaseURL:          baseURL,
		AnthropicBaseURL: strings.TrimSpace(r.FormValue("anthropic_base_url")),
		AnthropicVersion: strings.TrimSpace(r.FormValue("anthropic_version")),
		APIKey:           apiKey,
		APIKeyRef:        apiKeyRef,
		TimeoutSeconds:   existing.TimeoutSeconds,
	}
	if v := strings.TrimSpace(r.FormValue("timeout")); v != "" {
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil && parsed > 0 {
			providerCfg.TimeoutSeconds = parsed
		}
	}

	if err := providerCfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.store.UpsertProvider(r.Context(), providerCfg); err != nil {
		writeError(w, http.StatusBadRequest, "admin_provider_update_error", err.Error())
		return
	}
	if err := h.reloadProvidersAndModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_provider_reload_error", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/providers?flash=provider+updated", http.StatusSeeOther)
}

func (h *Handler) handleAdminProvidersDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	models, err := h.store.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_provider_delete_error", err.Error())
		return
	}
	for _, model := range models {
		if model.Provider == name {
			writeError(w, http.StatusBadRequest, "admin_provider_delete_error", "provider is still used by models")
			return
		}
	}
	if err := h.store.DeleteProvider(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_provider_delete_error", err.Error())
		return
	}
	if err := h.reloadProvidersAndModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_provider_reload_error", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/providers?flash=provider+deleted", http.StatusSeeOther)
}

func (h *Handler) handleAdminModelsPage(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	models, err := h.store.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_models_error", err.Error())
		return
	}
	data := adminViewData{
		Title:       adminPageTitle("Models"),
		IsAdmin:     session.Role == roleAdmin,
		CurrentUser: session.Username,
		Providers:   h.listProviderNames(),
		Models:      models,
		CurrentPath: "/admin/models",
		Flash:       r.URL.Query().Get("flash"),
	}
	_ = adminModelsTemplate.Execute(w, data)
}

func (h *Handler) handleAdminModelsSave(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	model := config.ModelConfig{
		PublicName:   strings.TrimSpace(r.FormValue("public_name")),
		Provider:     strings.TrimSpace(r.FormValue("provider")),
		UpstreamName: strings.TrimSpace(r.FormValue("upstream_name")),
	}
	if model.PublicName == "" || model.Provider == "" || model.UpstreamName == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "public_name, provider, upstream_name are required")
		return
	}
	if !containsString(h.listProviderNames(), model.Provider) {
		writeError(w, http.StatusBadRequest, "invalid_request", "provider not found")
		return
	}
	if err := h.store.UpsertModel(r.Context(), model); err != nil {
		writeError(w, http.StatusBadRequest, "admin_model_save_error", err.Error())
		return
	}
	if err := h.reloadModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_model_reload_error", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/models?flash=model+saved", http.StatusSeeOther)
}

func (h *Handler) handleAdminModelsDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	publicName := strings.TrimSpace(r.FormValue("public_name"))
	if publicName == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "public_name is required")
		return
	}
	if err := h.store.DeleteModel(r.Context(), publicName); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_model_delete_error", err.Error())
		return
	}
	if err := h.reloadModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "admin_model_reload_error", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/models?flash=model+deleted", http.StatusSeeOther)
}

func (h *Handler) handleAdminUsagePage(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	startStr := strings.TrimSpace(r.URL.Query().Get("start"))
	endStr := strings.TrimSpace(r.URL.Query().Get("end"))
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	keyID := strings.TrimSpace(r.URL.Query().Get("key"))
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	pieMetric := r.URL.Query().Get("pieMetric")
	if pieMetric != "requests" {
		pieMetric = "tokens"
	}

	keys, err := h.visibleKeys(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_usage_error", err.Error())
		return
	}
	usageKeys := buildUsageKeyOptions(keys)
	usageOwners := buildUsageOwnerOptions(keys)
	visibleKeyIDs := make(map[string]struct{}, len(usageKeys))
	for _, option := range usageKeys {
		visibleKeyIDs[option.Value] = struct{}{}
	}
	if keyID != "" {
		if _, ok := visibleKeyIDs[keyID]; !ok {
			keyID = ""
		}
	}

	filter := store.UsageFilter{
		Model: model,
		KeyID: keyID,
	}
	if session.Role != roleAdmin {
		filter.Owner = session.Username
		owner = ""
	} else if owner != "" {
		if containsString(usageOwners, owner) {
			filter.Owner = owner
		} else {
			owner = ""
		}
	}

	if startStr != "" {
		if t, err := time.ParseInLocation("2006-01-02", startStr, time.Local); err == nil {
			filter.StartTime = t
		}
	}
	if endStr != "" {
		if t, err := time.ParseInLocation("2006-01-02", endStr, time.Local); err == nil {
			filter.EndTime = t
		}
	}
	if filter.StartTime.IsZero() {
		now := time.Now()
		filter.StartTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		startStr = filter.StartTime.Format("2006-01-02")
	}
	if filter.EndTime.IsZero() {
		now := time.Now()
		filter.EndTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		endStr = filter.EndTime.Format("2006-01-02")
	}

	rollups, err := h.store.QueryUsageRollups(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_usage_error", err.Error())
		return
	}
	summaries := summarizeRollupsByKey(rollups)
	modelSummaries := summarizeRollupsByModel(rollups)
	trendHourPoints := summarizeRollupsTrend(rollups, "hour")
	trendDayPoints := summarizeRollupsTrend(rollups, "day")

	usageModels, err := h.store.ListUsageModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_usage_error", err.Error())
		return
	}

	data := adminViewData{
		Title:           adminPageTitle("Usage"),
		IsAdmin:         session.Role == roleAdmin,
		CurrentUser:     session.Username,
		Usage:           summaries,
		CurrentPath:     "/admin/usage/view",
		UsageModels:     usageModels,
		UsageKeys:       usageKeys,
		UsageOwners:     usageOwners,
		FilterStart:     startStr,
		FilterEnd:       endStr,
		FilterModel:     model,
		FilterKey:       keyID,
		FilterOwner:     owner,
		ModelSummaries:  modelSummaries,
		PieMetric:       pieMetric,
		TrendHourPoints: trendHourPoints,
		TrendDayPoints:  trendDayPoints,
	}
	_ = adminUsageTemplate.Execute(w, data)
}

func (h *Handler) handleAdminUsageTrend(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}

	startStr := strings.TrimSpace(r.URL.Query().Get("start"))
	endStr := strings.TrimSpace(r.URL.Query().Get("end"))
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	keyID := strings.TrimSpace(r.URL.Query().Get("key"))
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	groupBy := strings.TrimSpace(r.URL.Query().Get("groupBy"))

	filter := store.UsageFilter{
		Model: model,
		KeyID: keyID,
	}
	if session.Role != roleAdmin {
		filter.Owner = session.Username
	} else if owner != "" {
		filter.Owner = owner
	}

	if startStr != "" {
		if t, err := time.ParseInLocation("2006-01-02", startStr, time.Local); err == nil {
			filter.StartTime = t
		}
	}
	if endStr != "" {
		if t, err := time.ParseInLocation("2006-01-02", endStr, time.Local); err == nil {
			filter.EndTime = t
		}
	}
	if filter.StartTime.IsZero() {
		now := time.Now()
		filter.StartTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	}
	if filter.EndTime.IsZero() {
		now := time.Now()
		filter.EndTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	}

	points, err := h.store.QueryUsageTrend(r.Context(), filter, groupBy)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, points)
}

func (h *Handler) handleAdminPlaygroundPage(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	data, err := h.buildPlaygroundViewData(r.Context(), r, session, "", "", false, false, false, "", "", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
		return
	}
	_ = adminPlaygroundTemplate.Execute(w, data)
}

func (h *Handler) handleAdminPlaygroundChat(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}

	key := strings.TrimSpace(r.FormValue("api_key"))
	model := strings.TrimSpace(r.FormValue("model"))
	message := strings.TrimSpace(r.FormValue("message"))
	stream := r.FormValue("stream") == "on"
	apiStyle := r.FormValue("api_style")
	apiType := r.FormValue("api_type")
	if apiStyle == "" {
		apiStyle = "openai"
	}
	useResponses := apiStyle == "openai" && apiType == "responses"
	useAnthropic := apiStyle == "anthropic"

	if key == "" || model == "" || message == "" {
		data, err := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, useAnthropic, useResponses, message, "", "api_key, model and message are required")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}

	principal, ok := h.authenticateAPIKeyValue(key)
	if !ok {
		data, err := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, useAnthropic, useResponses, message, "", "invalid api key")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}
	if session.Role != roleAdmin && principal.Owner != session.Username {
		data, err := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, useAnthropic, useResponses, message, "", "selected api key is not owned by current user")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}

	target, err := h.router.Resolve(model)
	if err != nil {
		data, buildErr := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, useAnthropic, useResponses, message, "", "model not found")
		if buildErr != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", buildErr.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}
	providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
	if err != nil {
		data, buildErr := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, useAnthropic, useResponses, message, "", err.Error())
		if buildErr != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", buildErr.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}

	chatReq := &provider.ChatRequest{
		Model:  model,
		Stream: stream,
		Raw: map[string]any{
			"model": model,
			"messages": []map[string]any{
				{"role": "user", "content": message},
			},
			"stream": stream,
		},
	}

	start := time.Now()
	var output string
	if stream {
		streamResp, err := h.client.ChatStream(r.Context(), providerCfg, chatReq, target.UpstreamModel)
		if err != nil {
			logger.L.Error("admin playground chat failed", "op", "admin_playground_chat", "stream", true, "provider", target.ProviderName, "error", err)
			h.recordUsage(principal, "chat.completions", target.ProviderName, model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			data, buildErr := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, useAnthropic, useResponses, message, "", err.Error())
			if buildErr != nil {
				writeError(w, http.StatusInternalServerError, "admin_playground_error", buildErr.Error())
				return
			}
			_ = adminPlaygroundTemplate.Execute(w, data)
			return
		}
		defer streamResp.Body.Close()
		body, err := io.ReadAll(streamResp.Body)
		if err != nil {
			data, buildErr := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, useAnthropic, useResponses, message, "", err.Error())
			if buildErr != nil {
				writeError(w, http.StatusInternalServerError, "admin_playground_error", buildErr.Error())
				return
			}
			_ = adminPlaygroundTemplate.Execute(w, data)
			return
		}
		output = strings.TrimSpace(string(body))
		success := streamResp.StatusCode >= 200 && streamResp.StatusCode < 300
		h.recordUsage(principal, "chat.completions", target.ProviderName, model, target.UpstreamModel, success, 0, 0, 0, streamResp.StatusCode, time.Since(start))
		if !success {
			output = fmt.Sprintf("upstream status %d\n%s", streamResp.StatusCode, output)
		}
	} else {
		resp, err := h.client.Chat(r.Context(), providerCfg, chatReq, target.UpstreamModel)
		if err != nil {
			logger.L.Error("admin playground chat failed", "op", "admin_playground_chat", "stream", false, "provider", target.ProviderName, "error", err)
			h.recordUsage(principal, "chat.completions", target.ProviderName, model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			data, buildErr := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, useAnthropic, useResponses, message, "", err.Error())
			if buildErr != nil {
				writeError(w, http.StatusInternalServerError, "admin_playground_error", buildErr.Error())
				return
			}
			_ = adminPlaygroundTemplate.Execute(w, data)
			return
		}
		requestTokens, responseTokens, totalTokens := usage.ExtractUsage(map[string]any(*resp))
		h.recordUsage(principal, "chat.completions", target.ProviderName, model, target.UpstreamModel, true, requestTokens, responseTokens, totalTokens, http.StatusOK, time.Since(start))
		output = extractChatText(resp)
		if output == "" {
			pretty, _ := json.MarshalIndent(resp, "", "  ")
			output = string(pretty)
		}
	}

	data, err := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, useAnthropic, useResponses, message, output, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
		return
	}
	data.Flash = "request finished"
	_ = adminPlaygroundTemplate.Execute(w, data)
}

func (h *Handler) handleAdminPlaygroundChatAJAX(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid form"})
		return
	}

	key := strings.TrimSpace(r.PostFormValue("api_key"))
	model := strings.TrimSpace(r.PostFormValue("model"))
	message := strings.TrimSpace(r.PostFormValue("message"))
	stream := r.PostFormValue("stream") == "on"
	apiStyle := r.PostFormValue("api_style")
	apiType := r.PostFormValue("api_type")
	if apiStyle == "" {
		apiStyle = "openai"
	}
	if apiType == "" {
		apiType = "chat_completions"
	}

	if key == "" || model == "" || message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "api_key, model and message are required"})
		return
	}

	principal, ok := h.authenticateAPIKeyValue(key)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid api key"})
		return
	}
	if session.Role != roleAdmin && principal.Owner != session.Username {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "selected api key is not owned by current user"})
		return
	}

	// Resolve model → provider so we can call the client directly.
	target, err := h.router.Resolve(model)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model not found: " + model})
		return
	}
	providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	// Stream mode: call provider client directly, proxy SSE to client in real-time.
	if stream {
		h.handleAdminPlaygroundChatAJAXStream(w, r, apiStyle, apiType, model, message, providerCfg, target.UpstreamModel)
		return
	}

	// Non-stream: call our own internal API handler for identical routing/usage recording.
	var apiPath string
	var reqBody io.Reader

	if apiStyle == "anthropic" {
		apiPath = "/anthropic/v1/messages"
		body, _ := json.Marshal(map[string]any{
			"model":      model,
			"messages":   []map[string]any{{"role": "user", "content": message}},
			"max_tokens": 4096,
		})
		reqBody = bytes.NewReader(body)
	} else if apiType == "responses" {
		apiPath = "/v1/responses"
		body, _ := json.Marshal(map[string]any{
			"model": model,
			"input": message,
		})
		reqBody = bytes.NewReader(body)
	} else {
		apiPath = "/v1/chat/completions"
		body, _ := json.Marshal(map[string]any{
			"model": model,
			"messages": []map[string]any{
				{"role": "user", "content": message},
			},
		})
		reqBody = bytes.NewReader(body)
	}

	apiReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, apiPath, reqBody)
	apiReq.Header.Set("Authorization", "Bearer "+key)
	apiReq.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, apiReq)

	resp := rec.Result()
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		output := extractAPIResponseText(apiPath, respBody)
		if output == "" {
			output = strings.TrimSpace(string(respBody))
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": output})
	} else {
		if apiStyle == "anthropic" {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": strings.TrimSpace(string(respBody))})
		} else {
			var errResp struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(respBody, &errResp); err != nil || errResp.Error.Message == "" {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": strings.TrimSpace(string(respBody))})
			} else {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": errResp.Error.Message})
			}
		}
	}
}

func (h *Handler) handleAdminPlaygroundChatAJAXStream(w http.ResponseWriter, r *http.Request, apiStyle, apiType, model, message string, providerCfg config.ProviderConfig, upstreamModel string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	chatReq := &provider.ChatRequest{
		Model:  model,
		Stream: true,
		Raw: map[string]any{
			"model":    model,
			"messages": []map[string]any{{"role": "user", "content": message}},
			"stream":   true,
		},
	}
	if apiStyle == "anthropic" {
		chatReq.Raw["max_tokens"] = 4096
	} else if apiType == "responses" {
		chatReq.Raw = map[string]any{
			"model":  model,
			"input":  message,
			"stream": true,
		}
	}

	var streamResp *provider.StreamResponse
	var err error
	switch {
	case apiStyle == "anthropic":
		streamResp, err = h.client.MessagesStream(r.Context(), providerCfg, chatReq, upstreamModel)
	case apiType == "responses":
		streamResp, err = h.client.ResponsesStream(r.Context(), providerCfg, chatReq, upstreamModel)
	default:
		streamResp, err = h.client.ChatStream(r.Context(), providerCfg, chatReq, upstreamModel)
	}
	if err != nil {
		h.writeSSEEvent(w, flusher, "error", map[string]any{"message": err.Error()})
		return
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode < 200 || streamResp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(streamResp.Body)
		h.writeSSEEvent(w, flusher, "error", map[string]any{"message": strings.TrimSpace(string(bodyBytes)), "status": streamResp.StatusCode})
		return
	}

	// Parse SSE and forward both raw lines and parsed content events.
	// Upstream lines are wrapped as "event: raw" so the frontend can cleanly
	// separate raw upstream SSE from our custom content/error/done events.
	partialLine := ""
	buf := make([]byte, 4096)
	for {
		n, readErr := streamResp.Body.Read(buf)
		if n > 0 {
			chunk := partialLine + string(buf[:n])
			lines := strings.SplitAfter(chunk, "\n")
			partialLine = ""
			if len(lines[len(lines)-1]) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
				partialLine = lines[len(lines)-1]
				lines = lines[:len(lines)-1]
			}
			for _, line := range lines {
				trimmed := strings.TrimRight(line, "\r\n")
				if trimmed == "" {
					continue
				}
				// Wrap each upstream line as a "raw" event for the frontend.
				h.writeSSEEvent(w, flusher, "raw", map[string]any{"line": trimmed})

				// Parse data: lines for content extraction.
				if strings.HasPrefix(trimmed, "data:") {
					payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
					if payload != "" && payload != "[DONE]" {
						var raw map[string]any
						if err := json.Unmarshal([]byte(payload), &raw); err == nil {
							text := extractSSEContentText(apiStyle, apiType, raw)
							if text != "" {
								h.writeSSEEvent(w, flusher, "content", map[string]any{"text": text})
							}
						}
					} else if payload == "[DONE]" {
						h.writeSSEEvent(w, flusher, "done", map[string]any{})
					}
				}
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				logger.L.Warn("stream read error", "error", readErr)
			}
			break
		}
	}
}

func (h *Handler) writeSSEEvent(w io.Writer, flusher http.Flusher, event string, data any) {
	if data == nil {
		data = map[string]any{}
	}
	dataBytes, _ := json.Marshal(data)
	io.WriteString(w, "event: "+event+"\n")
	io.WriteString(w, "data: "+string(dataBytes)+"\n")
	io.WriteString(w, "\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func extractSSEContentText(apiStyle, apiType string, raw map[string]any) string {
	if apiStyle == "anthropic" {
		if t, _ := raw["type"].(string); t == "content_block_delta" {
			if delta, ok := raw["delta"].(map[string]any); ok {
				if text, ok := delta["text"].(string); ok {
					return text
				}
			}
		}
		return ""
	}
	if apiType == "responses" {
		if t, _ := raw["type"].(string); t == "response.output_text.delta" {
			if text, ok := raw["delta"].(string); ok {
				return text
			}
		}
		return ""
	}
	// OpenAI Chat Completions
	if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
		if c, ok := choices[0].(map[string]any); ok {
			if delta, ok := c["delta"].(map[string]any); ok {
				if text, ok := delta["content"].(string); ok {
					return text
				}
			}
		}
	}
	return ""
}

func (h *Handler) buildPlaygroundViewData(ctx context.Context, r *http.Request, session webSession, selectedKey, selectedModel string, stream, useAnthropic, useResponses bool, message, result, errMsg string) (adminViewData, error) {
	keys, err := h.visibleKeys(ctx, session)
	if err != nil {
		return adminViewData{}, err
	}
	models := h.router.ListModels()

	if selectedKey == "" && len(keys) > 0 {
		selectedKey = keys[0].Key
	}
	if selectedModel == "" && len(models) > 0 {
		selectedModel = models[0]
	}
	if strings.TrimSpace(message) == "" {
		message = "hi"
	}

	// Check if any provider has anthropic_base_url configured
	hasAnthropic := false
	providers, _ := h.store.ListProviders(ctx)
	for _, p := range providers {
		if strings.TrimSpace(p.AnthropicBaseURL) != "" {
			hasAnthropic = true
			break
		}
	}

	return adminViewData{
		Title:                adminPageTitle("Playground"),
		IsAdmin:              session.Role == roleAdmin,
		CurrentUser:          session.Username,
		CurrentPath:          "/admin/playground",
		Keys:                 keys,
		PlayModels:           models,
		PlayAPIBase:          publicAPIBaseURL(r),
		PlayAPIKey:           selectedKey,
		PlayModel:            selectedModel,
		PlayMessage:          strings.TrimSpace(message),
		PlayStream:           stream,
		PlayUseAnthropic:     useAnthropic,
		PlayUseResponses:     useResponses,
		PlayResult:           result,
		PlayError:            errMsg,
		HasAnthropicProvider: hasAnthropic,
	}, nil
}

func (h *Handler) authenticateAPIKeyValue(key string) (auth.Principal, bool) {
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	return h.auth.Authenticate(req)
}

func publicAPIBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
		parts := strings.Split(forwardedProto, ",")
		if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
			scheme = strings.TrimSpace(parts[0])
		}
	}
	return scheme + "://" + r.Host
}

func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto != "" {
		parts := strings.Split(forwardedProto, ",")
		return strings.EqualFold(strings.TrimSpace(parts[0]), "https")
	}
	return false
}

func extractAPIResponseText(path string, body []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	switch {
	case strings.Contains(path, "/messages"):
		resp := provider.AnthropicResponse(raw)
		return extractAnthropicText(&resp)
	case strings.Contains(path, "/responses"):
		resp := provider.OpenAIResponse(raw)
		return extractResponsesText(&resp)
	default:
		resp := provider.OpenAIResponse(raw)
		return extractChatText(&resp)
	}
}

func extractChatText(resp *provider.OpenAIResponse) string {
	if resp == nil {
		return ""
	}
	rawChoices, ok := (*resp)["choices"].([]any)
	if !ok || len(rawChoices) == 0 {
		return ""
	}
	firstChoice, ok := rawChoices[0].(map[string]any)
	if !ok {
		return ""
	}
	message, ok := firstChoice["message"].(map[string]any)
	if !ok {
		return ""
	}
	content, _ := message["content"].(string)
	return strings.TrimSpace(content)
}

func extractResponsesText(resp *provider.OpenAIResponse) string {
	if resp == nil {
		return ""
	}
	rawOutput, ok := (*resp)["output"].([]any)
	if !ok || len(rawOutput) == 0 {
		return ""
	}
	var texts []string
	for _, item := range rawOutput {
		itemMap, ok := item.(map[string]any)
		if !ok || itemMap["type"] != "message" {
			continue
		}
		content, ok := itemMap["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range content {
			partMap, ok := part.(map[string]any)
			if !ok || partMap["type"] != "output_text" {
				continue
			}
			if text, ok := partMap["text"].(string); ok {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "")
}

func extractAnthropicText(resp *provider.AnthropicResponse) string {
	if resp == nil {
		return ""
	}
	content, ok := (*resp)["content"].([]any)
	if !ok || len(content) == 0 {
		return ""
	}
	var texts []string
	for _, c := range content {
		block, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if blockType, ok := block["type"].(string); ok && blockType == "text" {
			if text, ok := block["text"].(string); ok {
				texts = append(texts, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

func (h *Handler) webSession(r *http.Request) (webSession, bool) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return webSession{}, false
	}
	return h.sessions.Get(cookie.Value)
}

func (h *Handler) requireWebSession(w http.ResponseWriter, r *http.Request) (webSession, bool) {
	session, ok := h.webSession(r)
	if ok {
		return session, true
	}
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
	return webSession{}, false
}

func (h *Handler) requireAdminSession(w http.ResponseWriter, r *http.Request) bool {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return false
	}
	if session.Role == roleAdmin {
		return true
	}
	writeError(w, http.StatusForbidden, "forbidden", "admin required")
	return false
}

func (h *Handler) adminAuthed(r *http.Request) bool {
	session, ok := h.webSession(r)
	return ok && session.Role == roleAdmin
}

func buildUsageKeyOptions(keys []config.KeyConfig) []adminUsageKeyOption {
	nameCounts := make(map[string]int)
	nameOwnerCounts := make(map[string]int)
	for _, key := range keys {
		if key.Name == "" {
			continue
		}
		nameCounts[key.Name]++
		nameOwnerCounts[key.Name+"\x00"+key.Owner]++
	}

	options := make([]adminUsageKeyOption, 0, len(keys))
	for _, key := range keys {
		keyID := usage.KeyID(key.Key)
		label := key.Name
		if label == "" {
			label = maskIdentifier(keyID)
		} else if nameCounts[key.Name] > 1 {
			if key.Owner != "" {
				label += " / " + key.Owner
			}
			if key.Owner == "" || nameOwnerCounts[key.Name+"\x00"+key.Owner] > 1 {
				label += " / " + maskIdentifier(keyID)
			}
		}
		options = append(options, adminUsageKeyOption{
			Value: keyID,
			Label: label,
		})
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Label == options[j].Label {
			return options[i].Value < options[j].Value
		}
		return options[i].Label < options[j].Label
	})
	return options
}

func buildUsageOwnerOptions(keys []config.KeyConfig) []string {
	set := make(map[string]struct{})
	for _, key := range keys {
		if key.Owner == "" {
			continue
		}
		set[key.Owner] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for owner := range set {
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}

func summarizeRollupsTrend(rollups []usage.Rollup, groupBy string) []store.TrendPoint {
	type agg struct {
		requestCount   int64
		successCount   int64
		errorCount     int64
		requestTokens  int64
		responseTokens int64
		totalTokens    int64
	}

	orderedKeys := make([]string, 0)
	groups := make(map[string]*agg)
	for _, rollup := range rollups {
		local := rollup.BucketStart.In(time.Local)
		key := local.Format("2006-01-02")
		if groupBy == "hour" {
			key = local.Format("2006-01-02 15:00")
		}
		if _, ok := groups[key]; !ok {
			groups[key] = &agg{}
			orderedKeys = append(orderedKeys, key)
		}
		group := groups[key]
		group.requestCount += rollup.RequestCount
		group.successCount += rollup.SuccessCount
		group.errorCount += rollup.ErrorCount
		group.requestTokens += rollup.RequestTokens
		group.responseTokens += rollup.ResponseTokens
		group.totalTokens += rollup.TotalTokens
	}

	out := make([]store.TrendPoint, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		group := groups[key]
		out = append(out, store.TrendPoint{
			Date:           key,
			RequestCount:   group.requestCount,
			SuccessCount:   group.successCount,
			ErrorCount:     group.errorCount,
			RequestTokens:  group.requestTokens,
			ResponseTokens: group.responseTokens,
			TotalTokens:    group.totalTokens,
		})
	}
	return out
}

func summarizeRollupsByKey(rollups []usage.Rollup) []usage.Summary {
	summaries := make(map[string]*usage.Summary)
	for _, rollup := range rollups {
		summary, ok := summaries[rollup.KeyID]
		if !ok {
			summary = &usage.Summary{
				KeyID:   rollup.KeyID,
				KeyName: rollup.KeyName,
				Owner:   rollup.Owner,
				Purpose: rollup.Purpose,
			}
			summaries[rollup.KeyID] = summary
		}
		if summary.KeyName == "" {
			summary.KeyName = rollup.KeyName
		}
		if summary.Owner == "" {
			summary.Owner = rollup.Owner
		}
		if summary.Purpose == "" {
			summary.Purpose = rollup.Purpose
		}
		summary.RequestCount += rollup.RequestCount
		summary.SuccessCount += rollup.SuccessCount
		summary.ErrorCount += rollup.ErrorCount
		summary.RequestTokens += rollup.RequestTokens
		summary.ResponseTokens += rollup.ResponseTokens
		summary.TotalTokens += rollup.TotalTokens
	}

	out := make([]usage.Summary, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].KeyID == out[j].KeyID {
			return out[i].KeyName < out[j].KeyName
		}
		return out[i].KeyID < out[j].KeyID
	})
	return out
}

func summarizeRollupsByModel(rollups []usage.Rollup) []usage.ModelSummary {
	summaries := make(map[string]*usage.ModelSummary)
	keySets := make(map[string]map[string]struct{})
	for _, rollup := range rollups {
		if rollup.PublicModel == "" {
			continue
		}
		summary, ok := summaries[rollup.PublicModel]
		if !ok {
			summary = &usage.ModelSummary{Model: rollup.PublicModel}
			summaries[rollup.PublicModel] = summary
		}
		summary.RequestCount += rollup.RequestCount
		summary.SuccessCount += rollup.SuccessCount
		summary.ErrorCount += rollup.ErrorCount
		summary.RequestTokens += rollup.RequestTokens
		summary.ResponseTokens += rollup.ResponseTokens
		summary.TotalTokens += rollup.TotalTokens
		if _, ok := keySets[rollup.PublicModel]; !ok {
			keySets[rollup.PublicModel] = make(map[string]struct{})
		}
		keySets[rollup.PublicModel][rollup.KeyID] = struct{}{}
	}

	out := make([]usage.ModelSummary, 0, len(summaries))
	for model, summary := range summaries {
		summary.KeyCount = int64(len(keySets[model]))
		out = append(out, *summary)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RequestCount == out[j].RequestCount {
			return out[i].Model < out[j].Model
		}
		return out[i].RequestCount > out[j].RequestCount
	})
	return out
}

func (h *Handler) visibleKeys(ctx context.Context, session webSession) ([]config.KeyConfig, error) {
	if session.Role == roleAdmin {
		return h.store.ListAuthKeys(ctx)
	}
	return h.store.ListAuthKeysByOwner(ctx, session.Username)
}

func filterUsageByOwner(summaries []usage.Summary, owner string) []usage.Summary {
	filtered := make([]usage.Summary, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Owner == owner {
			filtered = append(filtered, summary)
		}
	}
	return filtered
}

func (h *Handler) reloadModels(ctx context.Context) error {
	models, err := h.store.ListModels(ctx)
	if err != nil {
		return err
	}
	return h.router.UpdateModels(models)
}

func (h *Handler) reloadProvidersAndModels(ctx context.Context) error {
	models, err := h.store.ListModels(ctx)
	if err != nil {
		return err
	}
	providerConfigs, err := h.store.ListProviders(ctx)
	if err != nil {
		return err
	}
	if err := h.router.UpdateModels(models); err != nil {
		return err
	}
	names := make([]string, 0, len(providerConfigs))
	for _, providerCfg := range providerConfigs {
		names = append(names, providerCfg.Name)
	}
	h.setProviderNames(names)
	return nil
}

func (h *Handler) reloadAuthKeys(ctx context.Context) error {
	keys, err := h.store.ListAuthKeys(ctx)
	if err != nil {
		return err
	}
	h.auth.Update(keys)
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
