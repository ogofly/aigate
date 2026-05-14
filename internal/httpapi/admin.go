package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"aigate/internal/config"
	"aigate/internal/router"
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
	ModelRouteGroups     []adminModelRouteGroup
	Routing              config.RoutingConfig
	Usage                []usage.Summary
	CurrentPath          string
	Flash                string
	DefaultPublic        string
	PlayModels           []string
	PlayModelProviders   map[string]map[string][]string
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
	UsageProviders       []string
	UsageKeys            []adminUsageKeyOption
	UsageOwners          []string
	FilterStart          string
	FilterEnd            string
	FilterModel          string
	FilterProvider       string
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

type adminHomeViewData struct {
	Title            string
	IsAdmin          bool
	CurrentUser      string
	CurrentPath      string
	TodayLabel       string
	TodayRequests    int64
	TodayErrors      int64
	TodayTokens      int64
	ActiveKeys       int
	UsersTotal       int
	ProvidersEnabled int
	ProvidersTotal   int
	ModelsEnabled    int
	ModelsTotal      int
	RoutesEnabled    int
	RoutesTotal      int
	KeysTotal        int
	TrendHourPoints  []store.TrendPoint
	TopProviders     []adminHomeUsageSlice
	TopModels        []adminHomeUsageSlice
	TopKeys          []adminHomeUsageSlice
}

type adminHomeUsageSlice struct {
	Name        string `json:"name"`
	FilterValue string `json:"filter_value,omitempty"`
	Requests    int64  `json:"requests"`
	Errors      int64  `json:"errors"`
	TotalTokens int64  `json:"tokens"`
}

type adminUsageKeyOption struct {
	Value string
	Label string
}

type adminModelRouteGroup struct {
	PublicName string
	Routes     []config.ModelConfig
}

func groupModelsByPublicName(models []config.ModelConfig) []adminModelRouteGroup {
	sorted := append([]config.ModelConfig(nil), models...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].PublicName != sorted[j].PublicName {
			return sorted[i].PublicName < sorted[j].PublicName
		}
		if sorted[i].Provider != sorted[j].Provider {
			return sorted[i].Provider < sorted[j].Provider
		}
		if sorted[i].UpstreamName != sorted[j].UpstreamName {
			return sorted[i].UpstreamName < sorted[j].UpstreamName
		}
		return sorted[i].ID < sorted[j].ID
	})

	groups := make([]adminModelRouteGroup, 0)
	groupIndex := make(map[string]int)
	for _, model := range sorted {
		idx, ok := groupIndex[model.PublicName]
		if !ok {
			idx = len(groups)
			groupIndex[model.PublicName] = idx
			groups = append(groups, adminModelRouteGroup{PublicName: model.PublicName})
		}
		groups[idx].Routes = append(groups[idx].Routes, model)
	}
	return groups
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
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	data, err := h.buildAdminHomeViewData(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_home_error", err.Error())
		return
	}
	_ = adminHomeTemplate.Execute(w, data)
}

func (h *Handler) buildAdminHomeViewData(ctx context.Context, session webSession) (adminHomeViewData, error) {
	providers, err := h.store.ListProviders(ctx)
	if err != nil {
		return adminHomeViewData{}, err
	}
	models, err := h.store.ListModels(ctx)
	if err != nil {
		return adminHomeViewData{}, err
	}
	keys, err := h.visibleKeys(ctx, session)
	if err != nil {
		return adminHomeViewData{}, err
	}

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	filter := store.UsageFilter{StartTime: todayStart, EndTime: todayStart}
	if session.Role != roleAdmin {
		filter.Owner = session.Username
	}
	rollups, err := h.store.QueryUsageRollups(ctx, filter)
	if err != nil {
		return adminHomeViewData{}, err
	}

	var todayRequests, todayErrors, todayTokens int64
	for _, rollup := range rollups {
		todayRequests += rollup.RequestCount
		todayErrors += rollup.ErrorCount
		todayTokens += rollup.TotalTokens
	}

	providersEnabled := 0
	for _, providerCfg := range providers {
		if providerCfg.Enabled {
			providersEnabled++
		}
	}

	publicModelSet := make(map[string]struct{})
	enabledPublicModelSet := make(map[string]struct{})
	routesEnabled := 0
	for _, model := range models {
		if model.PublicName != "" {
			publicModelSet[model.PublicName] = struct{}{}
		}
		if model.Enabled {
			routesEnabled++
			if model.PublicName != "" {
				enabledPublicModelSet[model.PublicName] = struct{}{}
			}
		}
	}
	userSet := make(map[string]struct{})
	for _, key := range keys {
		if key.Owner != "" {
			userSet[key.Owner] = struct{}{}
		}
	}

	return adminHomeViewData{
		Title:            adminPageTitle("Home"),
		IsAdmin:          session.Role == roleAdmin,
		CurrentUser:      session.Username,
		CurrentPath:      "/admin",
		TodayLabel:       todayStart.Format("2006-01-02"),
		TodayRequests:    todayRequests,
		TodayErrors:      todayErrors,
		TodayTokens:      todayTokens,
		ActiveKeys:       len(keys),
		UsersTotal:       len(userSet),
		ProvidersEnabled: providersEnabled,
		ProvidersTotal:   len(providers),
		ModelsEnabled:    len(enabledPublicModelSet),
		ModelsTotal:      len(publicModelSet),
		RoutesEnabled:    routesEnabled,
		RoutesTotal:      len(models),
		KeysTotal:        len(keys),
		TrendHourPoints:  summarizeTodayHourlyRollups(todayStart, rollups),
		TopProviders:     summarizeHomeUsageSlices(rollups, func(rollup usage.Rollup) string { return rollup.Provider }),
		TopModels:        summarizeHomeUsageSlices(rollups, func(rollup usage.Rollup) string { return rollup.PublicModel }),
		TopKeys:          summarizeHomeKeyUsageSlices(rollups),
	}, nil
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
	models, err := h.store.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_keys_error", err.Error())
		return
	}
	data := adminViewData{
		Title:            adminPageTitle("Keys"),
		IsAdmin:          session.Role == roleAdmin,
		CurrentUser:      session.Username,
		Keys:             keys,
		Models:           models,
		ModelRouteGroups: groupModelsByPublicName(models),
		APIBase:          publicAPIBaseURL(r),
		CurrentPath:      "/admin/keys",
		Flash:            r.URL.Query().Get("flash"),
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
		Key:           strings.TrimSpace(r.FormValue("key")),
		Name:          strings.TrimSpace(r.FormValue("name")),
		Owner:         strings.TrimSpace(r.FormValue("owner")),
		Purpose:       strings.TrimSpace(r.FormValue("purpose")),
		ModelAccess:   strings.TrimSpace(r.FormValue("model_access")),
		ModelRouteIDs: cleanRouteIDs(r.Form["model_route_ids"]),
	}
	if key.ModelAccess == "" {
		key.ModelAccess = "all"
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
			writeError(w, http.StatusForbidden, "forbidden", "cannot delete key of another user")
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
		Enabled:          r.FormValue("enabled") != "false",
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
		Enabled:          existing.Enabled,
	}
	if _, ok := r.Form["_enabled_present"]; ok {
		providerCfg.Enabled = r.FormValue("enabled") == "true"
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
	routingSettings, err := h.store.GetRoutingSettings(r.Context())
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
		Routing:     routingSettings,
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
		Enabled:      r.FormValue("enabled") != "false",
	}
	if v := strings.TrimSpace(r.FormValue("priority")); v != "" {
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil {
			model.Priority = parsed
		}
	}
	if v := strings.TrimSpace(r.FormValue("weight")); v != "" {
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil && parsed > 0 {
			model.Weight = parsed
		}
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
	modelID := strings.TrimSpace(r.FormValue("model_id"))
	if modelID == "" {
		modelID = strings.TrimSpace(r.FormValue("public_name"))
	}
	if modelID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model route id is required")
		return
	}
	if err := h.store.DeleteModel(r.Context(), modelID); err != nil {
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
	providerName := strings.TrimSpace(r.URL.Query().Get("provider"))
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
		Model:    model,
		Provider: providerName,
		KeyID:    keyID,
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
	usageProviders, err := h.store.ListUsageProviders(r.Context())
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
		UsageProviders:  usageProviders,
		UsageKeys:       usageKeys,
		UsageOwners:     usageOwners,
		FilterStart:     startStr,
		FilterEnd:       endStr,
		FilterModel:     model,
		FilterProvider:  providerName,
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
	providerName := strings.TrimSpace(r.URL.Query().Get("provider"))
	keyID := strings.TrimSpace(r.URL.Query().Get("key"))
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	groupBy := strings.TrimSpace(r.URL.Query().Get("groupBy"))

	filter := store.UsageFilter{
		Model:    model,
		Provider: providerName,
		KeyID:    keyID,
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

func (h *Handler) buildPlaygroundViewData(ctx context.Context, r *http.Request, session webSession, selectedKey, selectedModel string, stream, useAnthropic, useResponses bool, message, result, errMsg string) (adminViewData, error) {
	keys, err := h.visibleKeys(ctx, session)
	if err != nil {
		return adminViewData{}, err
	}
	models := h.router.ListModels()
	providerOptions := h.buildPlaygroundProviderOptions(keys, models)

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
		PlayModelProviders:   providerOptions,
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

func (h *Handler) buildPlaygroundProviderOptions(keys []config.KeyConfig, models []string) map[string]map[string][]string {
	out := make(map[string]map[string][]string, len(keys))
	for _, key := range keys {
		access := router.Access{
			ModelAccess:   key.ModelAccess,
			ModelRouteIDs: append([]string(nil), key.ModelRouteIDs...),
		}
		byModel := make(map[string][]string, len(models))
		for _, model := range models {
			byModel[model] = h.router.ListProvidersForAccess(model, access)
		}
		out[key.Key] = byModel
	}
	return out
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

func summarizeTodayHourlyRollups(todayStart time.Time, rollups []usage.Rollup) []store.TrendPoint {
	if len(rollups) == 0 {
		return nil
	}

	indexes := make(map[string]int, 24)
	points := make([]store.TrendPoint, 0, 24)
	for hour := 0; hour < 24; hour++ {
		key := todayStart.Add(time.Duration(hour) * time.Hour).Format("2006-01-02 15:00")
		points = append(points, store.TrendPoint{Date: key})
		indexes[key] = len(points) - 1
	}
	for _, rollup := range rollups {
		key := rollup.BucketStart.In(time.Local).Format("2006-01-02 15:00")
		index, ok := indexes[key]
		if !ok {
			continue
		}
		point := &points[index]
		point.RequestCount += rollup.RequestCount
		point.SuccessCount += rollup.SuccessCount
		point.ErrorCount += rollup.ErrorCount
		point.RequestTokens += rollup.RequestTokens
		point.ResponseTokens += rollup.ResponseTokens
		point.TotalTokens += rollup.TotalTokens
	}
	return points
}

func summarizeHomeUsageSlices(rollups []usage.Rollup, nameFn func(usage.Rollup) string) []adminHomeUsageSlice {
	const maxSlices = 8

	byName := make(map[string]*adminHomeUsageSlice)
	for _, rollup := range rollups {
		name := strings.TrimSpace(nameFn(rollup))
		if name == "" {
			name = "Unknown"
		}
		slice, ok := byName[name]
		if !ok {
			slice = &adminHomeUsageSlice{Name: name, FilterValue: name}
			byName[name] = slice
		}
		slice.Requests += rollup.RequestCount
		slice.Errors += rollup.ErrorCount
		slice.TotalTokens += rollup.TotalTokens
	}

	out := make([]adminHomeUsageSlice, 0, len(byName))
	for _, slice := range byName {
		out = append(out, *slice)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalTokens == out[j].TotalTokens {
			if out[i].Requests == out[j].Requests {
				return out[i].Name < out[j].Name
			}
			return out[i].Requests > out[j].Requests
		}
		return out[i].TotalTokens > out[j].TotalTokens
	})
	if len(out) <= maxSlices {
		return out
	}

	other := adminHomeUsageSlice{Name: "Other"}
	for _, slice := range out[maxSlices:] {
		other.Requests += slice.Requests
		other.Errors += slice.Errors
		other.TotalTokens += slice.TotalTokens
	}
	out = append(out[:maxSlices], other)
	return out
}

func summarizeHomeKeyUsageSlices(rollups []usage.Rollup) []adminHomeUsageSlice {
	const maxSlices = 8

	byKey := make(map[string]*adminHomeUsageSlice)
	for _, rollup := range rollups {
		keyID := strings.TrimSpace(rollup.KeyID)
		if keyID == "" {
			keyID = "Unknown"
		}
		name := homeKeyUsageName(rollup)
		slice, ok := byKey[keyID]
		if !ok {
			slice = &adminHomeUsageSlice{Name: name, FilterValue: keyID}
			byKey[keyID] = slice
		}
		slice.Requests += rollup.RequestCount
		slice.Errors += rollup.ErrorCount
		slice.TotalTokens += rollup.TotalTokens
	}

	out := make([]adminHomeUsageSlice, 0, len(byKey))
	for _, slice := range byKey {
		out = append(out, *slice)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalTokens == out[j].TotalTokens {
			if out[i].Requests == out[j].Requests {
				return out[i].Name < out[j].Name
			}
			return out[i].Requests > out[j].Requests
		}
		return out[i].TotalTokens > out[j].TotalTokens
	})
	if len(out) <= maxSlices {
		return out
	}

	other := adminHomeUsageSlice{Name: "Other"}
	for _, slice := range out[maxSlices:] {
		other.Requests += slice.Requests
		other.Errors += slice.Errors
		other.TotalTokens += slice.TotalTokens
	}
	out = append(out[:maxSlices], other)
	return out
}

func homeKeyUsageName(rollup usage.Rollup) string {
	if rollup.KeyName != "" {
		return rollup.KeyName
	}
	if rollup.KeyID != "" {
		return maskIdentifier(rollup.KeyID)
	}
	return "Unknown"
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
	providerConfigs, err := h.store.ListProviders(ctx)
	if err != nil {
		return err
	}
	settings, err := h.store.GetRoutingSettings(ctx)
	if err != nil {
		return err
	}
	return h.router.Update(models, providerConfigs, settings)
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
	settings, err := h.store.GetRoutingSettings(ctx)
	if err != nil {
		return err
	}
	if err := h.router.Update(models, providerConfigs, settings); err != nil {
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
