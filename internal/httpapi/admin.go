package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"aigate/internal/auth"
	"aigate/internal/config"
	"aigate/internal/provider"
	"aigate/internal/usage"
)

const adminSessionCookie = "aigate_admin_session"

type webRole string

const (
	roleAdmin webRole = "admin"
	roleUser  webRole = "user"
)

type webSession struct {
	Role     webRole
	Username string
}

type adminSessionStore struct {
	mu     sync.RWMutex
	tokens map[string]webSession
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
	s.mu.Lock()
	s.tokens[token] = session
	s.mu.Unlock()
	return token, nil
}

func (s *adminSessionStore) Get(token string) (webSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.tokens[token]
	return session, ok
}

func (s *adminSessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

type adminViewData struct {
	Title         string
	Error         string
	IsAdmin       bool
	CurrentUser   string
	Keys          []config.KeyConfig
	ProvidersCfg  []config.ProviderConfig
	Providers     []string
	Models        []config.ModelConfig
	Usage         []usage.Summary
	CurrentPath   string
	Flash         string
	DefaultPublic string
	PlayModels    []string
	PlayAPIBase   string
	PlayAPIKey    string
	PlayModel     string
	PlayMessage   string
	PlayStream    bool
	PlayResult    string
	PlayError     string
	APIBase       string
}

func (h *Handler) handleAdminLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.webSession(r); ok {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	_ = adminLoginTemplate.Execute(w, adminViewData{Title: "Login"})
}

func (h *Handler) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		_ = adminLoginTemplate.Execute(w, adminViewData{Title: "Login", Error: "invalid form"})
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
			_ = adminLoginTemplate.Execute(w, adminViewData{Title: "Login", Error: "invalid credentials"})
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
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *Handler) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminSessionCookie); err == nil {
		h.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   adminSessionCookie,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *Handler) handleAdminHome(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	if session.Role == roleAdmin {
		http.Redirect(w, r, "/admin/providers", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/keys", http.StatusSeeOther)
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
		Title:       "Keys",
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
		Title:        "Providers",
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
	timeoutSeconds := 60
	if v := strings.TrimSpace(r.FormValue("timeout")); v != "" {
		var parsed int
		if _, err := fmt.Sscanf(v, "%d", &parsed); err == nil && parsed > 0 {
			timeoutSeconds = parsed
		}
	}
	providerCfg := config.ProviderConfig{
		Name:             strings.TrimSpace(r.FormValue("name")),
		APIKey:           strings.TrimSpace(r.FormValue("api_key")),
		BaseURL:          strings.TrimSpace(r.FormValue("base_url")),
		AnthropicBaseURL: strings.TrimSpace(r.FormValue("anthropic_base_url")),
		AnthropicVersion: strings.TrimSpace(r.FormValue("anthropic_version")),
		APIKeyRef:        strings.TrimSpace(r.FormValue("api_key_ref")),
		TimeoutSeconds:   timeoutSeconds,
	}
	if providerCfg.Name == "" || providerCfg.BaseURL == "" || (providerCfg.APIKey == "" && providerCfg.APIKeyRef == "") {
		writeError(w, http.StatusBadRequest, "invalid_request", "name, base_url, and api_key or api_key_ref are required")
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
		Title:       "Models",
		IsAdmin:     session.Role == roleAdmin,
		CurrentUser: session.Username,
		Providers:   h.providerNames,
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
	if !containsString(h.providerNames, model.Provider) {
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
	summaries := h.usage.Summaries()
	if session.Role != roleAdmin {
		summaries = filterUsageByOwner(summaries, session.Username)
	}
	data := adminViewData{
		Title:       "Usage",
		IsAdmin:     session.Role == roleAdmin,
		CurrentUser: session.Username,
		Usage:       summaries,
		CurrentPath: "/admin/usage/view",
	}
	_ = adminUsageTemplate.Execute(w, data)
}

func (h *Handler) handleAdminPlaygroundPage(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	data, err := h.buildPlaygroundViewData(r.Context(), r, session, "", "", false, "", "", "")
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

	if key == "" || model == "" || message == "" {
		data, err := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, message, "", "api_key, model and message are required")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}

	principal, ok := h.authenticateAPIKeyValue(key)
	if !ok {
		data, err := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, message, "", "invalid api key")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}
	if session.Role != roleAdmin && principal.Owner != session.Username {
		data, err := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, message, "", "selected api key is not owned by current user")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}

	target, err := h.router.Resolve(model)
	if err != nil {
		data, buildErr := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, message, "", "model not found")
		if buildErr != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", buildErr.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}
	providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
	if err != nil {
		data, buildErr := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, message, "", err.Error())
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
			log.Printf("method=%s path=%s op=admin_playground_chat stream=true provider=%s error=%v", r.Method, r.URL.Path, target.ProviderName, err)
			h.recordUsage(principal, "chat.completions", target.ProviderName, model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			data, buildErr := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, message, "", err.Error())
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
			data, buildErr := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, message, "", err.Error())
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
			log.Printf("method=%s path=%s op=admin_playground_chat stream=false provider=%s error=%v", r.Method, r.URL.Path, target.ProviderName, err)
			h.recordUsage(principal, "chat.completions", target.ProviderName, model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			data, buildErr := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, message, "", err.Error())
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

	data, err := h.buildPlaygroundViewData(r.Context(), r, session, key, model, stream, message, output, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
		return
	}
	data.Flash = "request finished"
	_ = adminPlaygroundTemplate.Execute(w, data)
}

func (h *Handler) buildPlaygroundViewData(ctx context.Context, r *http.Request, session webSession, selectedKey, selectedModel string, stream bool, message, result, errMsg string) (adminViewData, error) {
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

	return adminViewData{
		Title:       "Playground",
		IsAdmin:     session.Role == roleAdmin,
		CurrentUser: session.Username,
		CurrentPath: "/admin/playground",
		Keys:        keys,
		PlayModels:  models,
		PlayAPIBase: publicAPIBaseURL(r),
		PlayAPIKey:  selectedKey,
		PlayModel:   selectedModel,
		PlayMessage: strings.TrimSpace(message),
		PlayStream:  stream,
		PlayResult:  result,
		PlayError:   errMsg,
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
	return scheme + "://" + r.Host + "/v1"
}

func extractChatText(resp *provider.ChatResponse) string {
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
	h.providerNames = names
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
