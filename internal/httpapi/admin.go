package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
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

type adminSessionStore struct {
	mu     sync.RWMutex
	tokens map[string]struct{}
}

func newAdminSessionStore() *adminSessionStore {
	return &adminSessionStore{tokens: make(map[string]struct{})}
}

func (s *adminSessionStore) New() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	s.mu.Lock()
	s.tokens[token] = struct{}{}
	s.mu.Unlock()
	return token, nil
}

func (s *adminSessionStore) Has(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.tokens[token]
	return ok
}

func (s *adminSessionStore) Delete(token string) {
	s.mu.Lock()
	delete(s.tokens, token)
	s.mu.Unlock()
}

type adminViewData struct {
	Title         string
	Error         string
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
}

func (h *Handler) handleAdminLoginPage(w http.ResponseWriter, r *http.Request) {
	if h.adminAuthed(r) {
		http.Redirect(w, r, "/admin/models", http.StatusSeeOther)
		return
	}
	_ = adminLoginTemplate.Execute(w, adminViewData{Title: "Admin Login"})
}

func (h *Handler) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		_ = adminLoginTemplate.Execute(w, adminViewData{Title: "Admin Login", Error: "invalid form"})
		return
	}
	if r.FormValue("username") != h.admin.Username || r.FormValue("password") != h.admin.Password {
		_ = adminLoginTemplate.Execute(w, adminViewData{Title: "Admin Login", Error: "invalid credentials"})
		return
	}
	token, err := h.sessions.New()
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
	http.Redirect(w, r, "/admin/models", http.StatusSeeOther)
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
	http.Redirect(w, r, "/admin/providers", http.StatusSeeOther)
}

func (h *Handler) handleAdminKeysPage(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	keys, err := h.store.ListAuthKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_keys_error", err.Error())
		return
	}
	data := adminViewData{
		Title:       "Keys",
		Keys:        keys,
		CurrentPath: "/admin/keys",
		Flash:       r.URL.Query().Get("flash"),
	}
	_ = adminKeysTemplate.Execute(w, data)
}

func (h *Handler) handleAdminKeysSave(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
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
	if !h.requireAdminSession(w, r) {
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
	if !h.requireAdminSession(w, r) {
		return
	}
	providersCfg, err := h.store.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_providers_error", err.Error())
		return
	}
	data := adminViewData{
		Title:        "Providers",
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
		Name:           strings.TrimSpace(r.FormValue("name")),
		APIKey:         strings.TrimSpace(r.FormValue("api_key")),
		BaseURL:        strings.TrimSpace(r.FormValue("base_url")),
		APIKeyRef:      strings.TrimSpace(r.FormValue("api_key_ref")),
		TimeoutSeconds: timeoutSeconds,
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
	if !h.requireAdminSession(w, r) {
		return
	}
	models, err := h.store.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_models_error", err.Error())
		return
	}
	data := adminViewData{
		Title:       "Models",
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
	if !h.requireAdminSession(w, r) {
		return
	}
	data := adminViewData{
		Title:       "Usage",
		Usage:       h.usage.Summaries(),
		CurrentPath: "/admin/usage/view",
	}
	_ = adminUsageTemplate.Execute(w, data)
}

func (h *Handler) handleAdminPlaygroundPage(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	data, err := h.buildPlaygroundViewData(r.Context(), r, "", "", false, "", "", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
		return
	}
	_ = adminPlaygroundTemplate.Execute(w, data)
}

func (h *Handler) handleAdminPlaygroundChat(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
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
		data, err := h.buildPlaygroundViewData(r.Context(), r, key, model, stream, message, "", "api_key, model and message are required")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}

	principal, ok := h.authenticateAPIKeyValue(key)
	if !ok {
		data, err := h.buildPlaygroundViewData(r.Context(), r, key, model, stream, message, "", "invalid api key")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}

	target, err := h.router.Resolve(model)
	if err != nil {
		data, buildErr := h.buildPlaygroundViewData(r.Context(), r, key, model, stream, message, "", "model not found")
		if buildErr != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", buildErr.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}
	providerCfg, err := h.store.GetProvider(r.Context(), target.ProviderName)
	if err != nil {
		data, buildErr := h.buildPlaygroundViewData(r.Context(), r, key, model, stream, message, "", err.Error())
		if buildErr != nil {
			writeError(w, http.StatusInternalServerError, "admin_playground_error", buildErr.Error())
			return
		}
		_ = adminPlaygroundTemplate.Execute(w, data)
		return
	}

	chatReq := &provider.ChatRequest{
		Model: model,
		Messages: []provider.ChatMessage{
			{Role: "user", Content: message},
		},
		Stream: stream,
	}

	start := time.Now()
	var output string
	if stream {
		reader, err := h.client.ChatStream(r.Context(), providerCfg, chatReq, target.UpstreamModel)
		if err != nil {
			log.Printf("method=%s path=%s op=admin_playground_chat stream=true provider=%s error=%v", r.Method, r.URL.Path, target.ProviderName, err)
			h.recordUsage(principal, "chat.completions", target.ProviderName, model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			data, buildErr := h.buildPlaygroundViewData(r.Context(), r, key, model, stream, message, "", err.Error())
			if buildErr != nil {
				writeError(w, http.StatusInternalServerError, "admin_playground_error", buildErr.Error())
				return
			}
			_ = adminPlaygroundTemplate.Execute(w, data)
			return
		}
		defer reader.Close()
		body, err := io.ReadAll(reader)
		if err != nil {
			data, buildErr := h.buildPlaygroundViewData(r.Context(), r, key, model, stream, message, "", err.Error())
			if buildErr != nil {
				writeError(w, http.StatusInternalServerError, "admin_playground_error", buildErr.Error())
				return
			}
			_ = adminPlaygroundTemplate.Execute(w, data)
			return
		}
		output = strings.TrimSpace(string(body))
		h.recordUsage(principal, "chat.completions", target.ProviderName, model, target.UpstreamModel, true, 0, 0, 0, http.StatusOK, time.Since(start))
	} else {
		resp, err := h.client.Chat(r.Context(), providerCfg, chatReq, target.UpstreamModel)
		if err != nil {
			log.Printf("method=%s path=%s op=admin_playground_chat stream=false provider=%s error=%v", r.Method, r.URL.Path, target.ProviderName, err)
			h.recordUsage(principal, "chat.completions", target.ProviderName, model, target.UpstreamModel, false, 0, 0, 0, http.StatusBadGateway, time.Since(start))
			data, buildErr := h.buildPlaygroundViewData(r.Context(), r, key, model, stream, message, "", err.Error())
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

	data, err := h.buildPlaygroundViewData(r.Context(), r, key, model, stream, message, output, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_playground_error", err.Error())
		return
	}
	data.Flash = "request finished"
	_ = adminPlaygroundTemplate.Execute(w, data)
}

func (h *Handler) buildPlaygroundViewData(ctx context.Context, r *http.Request, selectedKey, selectedModel string, stream bool, message, result, errMsg string) (adminViewData, error) {
	keys, err := h.store.ListAuthKeys(ctx)
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

	return adminViewData{
		Title:       "Playground",
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

func (h *Handler) requireAdminSession(w http.ResponseWriter, r *http.Request) bool {
	if h.adminAuthed(r) {
		return true
	}
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
	return false
}

func (h *Handler) adminAuthed(r *http.Request) bool {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return false
	}
	return h.sessions.Has(cookie.Value)
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

var adminLoginTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:480px;margin:48px auto;padding:0 16px;color:#222;background:radial-gradient(circle at 0 0,#eef6ff 0,#f8fbff 36%,#f5f7fb 100%)}input{width:100%;padding:10px;margin:8px 0;border:1px solid #ccc;border-radius:8px}button{padding:10px 16px;border:0;background:#111;color:#fff;border-radius:8px} .error{color:#b00020;margin-bottom:12px}</style>
</head><body><h1>Admin Login</h1>{{if .Error}}<div class="error">{{.Error}}</div>{{end}}<form method="post" action="/admin/login"><input name="username" placeholder="username"><input type="password" name="password" placeholder="password"><button type="submit">Sign In</button></form></body></html>`))

var adminProvidersTemplate = template.Must(template.New("providers").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:1300px;margin:32px auto;padding:0 16px;color:#222;background:radial-gradient(circle at 0 0,#eef6ff 0,#f8fbff 36%,#f5f7fb 100%)}nav a{margin-right:16px;color:#111;text-decoration:none}table{width:100%;border-collapse:collapse;margin-top:24px}th,td{padding:10px;border-bottom:1px solid #eee;text-align:left}input{padding:10px;border:1px solid #ccc;border-radius:8px;margin-right:8px;min-width:140px}button{padding:8px 12px;border:0;background:#111;color:#fff;border-radius:8px}.muted{color:#666}.flash{padding:10px 12px;background:#edf7ed;border-radius:8px;margin-top:16px}.mono{font-family:ui-monospace,SFMono-Regular,monospace}</style>
</head><body><nav><a href="/admin/providers">Providers</a><a href="/admin/models">Models</a><a href="/admin/keys">Keys</a><a href="/admin/playground">Playground</a><a href="/admin/usage/view">Usage</a><form style="display:inline" method="post" action="/admin/logout"><button type="submit">Logout</button></form></nav><h1>Providers</h1>{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}<form method="post" action="/admin/providers" style="margin-top:20px"><input name="name" placeholder="name"><input name="base_url" placeholder="base url"><input name="api_key" placeholder="api key (optional)"><input name="api_key_ref" placeholder="env var name (optional)"><input name="timeout" placeholder="timeout seconds" value="60"><button type="submit">Save</button></form><div class="muted" style="margin-top:8px">Fill either <span class="mono">api_key</span> or <span class="mono">api_key_ref</span>.</div><table><thead><tr><th>Name</th><th>Base URL</th><th>API Key</th><th>Secret Ref</th><th>Timeout</th><th></th></tr></thead><tbody>{{range .ProvidersCfg}}<tr><td>{{.Name}}</td><td>{{.BaseURL}}</td><td>{{if .APIKey}}configured{{else}}-{{end}}</td><td>{{if .APIKeyRef}}{{.APIKeyRef}}{{else}}-{{end}}</td><td>{{.TimeoutSeconds}}s</td><td><form method="post" action="/admin/providers/delete"><input type="hidden" name="name" value="{{.Name}}"><button type="submit">Delete</button></form></td></tr>{{else}}<tr><td colspan="6" class="muted">No providers</td></tr>{{end}}</tbody></table></body></html>`))

var adminModelsTemplate = template.Must(template.New("models").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:1100px;margin:32px auto;padding:0 16px;color:#222;background:radial-gradient(circle at 0 0,#eef6ff 0,#f8fbff 36%,#f5f7fb 100%)}nav a{margin-right:16px;color:#111;text-decoration:none}table{width:100%;border-collapse:collapse;margin-top:24px}th,td{padding:10px;border-bottom:1px solid #eee;text-align:left}input,select{padding:10px;border:1px solid #ccc;border-radius:8px;margin-right:8px}button{padding:8px 12px;border:0;background:#111;color:#fff;border-radius:8px}.muted{color:#666}.flash{padding:10px 12px;background:#edf7ed;border-radius:8px;margin-top:16px}</style>
</head><body><nav><a href="/admin/providers">Providers</a><a href="/admin/models">Models</a><a href="/admin/keys">Keys</a><a href="/admin/playground">Playground</a><a href="/admin/usage/view">Usage</a><form style="display:inline" method="post" action="/admin/logout"><button type="submit">Logout</button></form></nav><h1>Models</h1>{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}<form method="post" action="/admin/models" style="margin-top:20px"><input name="public_name" placeholder="public name"><select name="provider">{{range .Providers}}<option value="{{.}}">{{.}}</option>{{end}}</select><input name="upstream_name" placeholder="upstream name"><button type="submit">Save</button></form><table><thead><tr><th>Public</th><th>Provider</th><th>Upstream</th><th></th></tr></thead><tbody>{{range .Models}}<tr><td>{{.PublicName}}</td><td>{{.Provider}}</td><td>{{.UpstreamName}}</td><td><form method="post" action="/admin/models/delete"><input type="hidden" name="public_name" value="{{.PublicName}}"><button type="submit">Delete</button></form></td></tr>{{else}}<tr><td colspan="4" class="muted">No models</td></tr>{{end}}</tbody></table></body></html>`))

var adminKeysTemplate = template.Must(template.New("keys").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:1200px;margin:32px auto;padding:0 16px;color:#222;background:radial-gradient(circle at 0 0,#eef6ff 0,#f8fbff 36%,#f5f7fb 100%)}nav a{margin-right:16px;color:#111;text-decoration:none}table{width:100%;border-collapse:collapse;margin-top:24px}th,td{padding:10px;border-bottom:1px solid #eee;text-align:left}input{padding:10px;border:1px solid #ccc;border-radius:8px;margin-right:8px;min-width:120px}label{margin-right:12px}button{padding:8px 12px;border:0;background:#111;color:#fff;border-radius:8px}.muted{color:#666}.flash{padding:10px 12px;background:#edf7ed;border-radius:8px;margin-top:16px}.key-cell{display:flex;align-items:center;gap:8px}.key-text{font-family:ui-monospace,SFMono-Regular,monospace}.ghost{background:#f3f3f3;color:#111}</style>
</head><body><nav><a href="/admin/providers">Providers</a><a href="/admin/models">Models</a><a href="/admin/keys">Keys</a><a href="/admin/playground">Playground</a><a href="/admin/usage/view">Usage</a><form style="display:inline" method="post" action="/admin/logout"><button type="submit">Logout</button></form></nav><h1>Keys</h1>{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}<form method="post" action="/admin/keys" style="margin-top:20px"><input name="key" placeholder="api key"><input name="name" placeholder="name"><input name="owner" placeholder="owner"><input name="purpose" placeholder="purpose"><button type="submit">Save</button></form><table><thead><tr><th>Name</th><th>Owner</th><th>Purpose</th><th>Key</th><th></th></tr></thead><tbody>{{range .Keys}}<tr><td>{{.Name}}</td><td>{{.Owner}}</td><td>{{.Purpose}}</td><td><div class="key-cell"><span class="key-text" data-key="{{.Key}}">****</span><button type="button" class="ghost" onclick="toggleKey(this)">Show</button><button type="button" class="ghost" onclick="copyKey(this)">Copy</button></div></td><td><form method="post" action="/admin/keys/delete"><input type="hidden" name="key" value="{{.Key}}"><button type="submit">Delete</button></form></td></tr>{{else}}<tr><td colspan="5" class="muted">No keys</td></tr>{{end}}</tbody></table><script>function toggleKey(button){const text=button.parentNode.querySelector('.key-text');const hidden=text.textContent==='****';text.textContent=hidden?text.dataset.key:'****';button.textContent=hidden?'Hide':'Show';}async function copyKey(button){const text=button.parentNode.querySelector('.key-text');const value=text.dataset.key;try{await navigator.clipboard.writeText(value);button.textContent='Copied';setTimeout(()=>{button.textContent='Copy';},1200);}catch(e){button.textContent='Copy failed';setTimeout(()=>{button.textContent='Copy';},1200);}}</script></body></html>`))

var adminUsageTemplate = template.Must(template.New("usage").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:1100px;margin:32px auto;padding:0 16px;color:#222;background:radial-gradient(circle at 0 0,#eef6ff 0,#f8fbff 36%,#f5f7fb 100%)}nav a{margin-right:16px;color:#111;text-decoration:none}table{width:100%;border-collapse:collapse;margin-top:24px}th,td{padding:10px;border-bottom:1px solid #eee;text-align:left}button{padding:8px 12px;border:0;background:#111;color:#fff;border-radius:8px}.muted{color:#666}</style>
</head><body><nav><a href="/admin/providers">Providers</a><a href="/admin/models">Models</a><a href="/admin/keys">Keys</a><a href="/admin/playground">Playground</a><a href="/admin/usage/view">Usage</a><form style="display:inline" method="post" action="/admin/logout"><button type="submit">Logout</button></form></nav><h1>Usage</h1><table><thead><tr><th>Key ID</th><th>Name</th><th>Owner</th><th>Purpose</th><th>Requests</th><th>Success</th><th>Errors</th><th>Total Tokens</th></tr></thead><tbody>{{range .Usage}}<tr><td>{{.KeyID}}</td><td>{{.KeyName}}</td><td>{{.Owner}}</td><td>{{.Purpose}}</td><td>{{.RequestCount}}</td><td>{{.SuccessCount}}</td><td>{{.ErrorCount}}</td><td>{{.TotalTokens}}</td></tr>{{else}}<tr><td colspan="8" class="muted">No usage yet</td></tr>{{end}}</tbody></table></body></html>`))

var adminPlaygroundTemplate = template.Must(template.New("playground").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>
:root{--bg:#f5f7fb;--card:#ffffff;--line:#e8ebf2;--text:#1a1f2b;--muted:#5f677a;--accent:#1f6feb}
*{box-sizing:border-box}
body{margin:0;background:radial-gradient(circle at 0 0,#eef6ff 0,#f8fbff 36%,#f5f7fb 100%);color:var(--text);font-family:-apple-system,BlinkMacSystemFont,sans-serif}
.wrap{max-width:1280px;margin:24px auto;padding:0 16px}
nav{display:flex;gap:14px;align-items:center;margin-bottom:18px}
nav a{text-decoration:none;color:#1b2430}
.logout{margin-left:auto}
button{border:0;background:#111;color:#fff;border-radius:10px;padding:9px 14px;cursor:pointer}
.ghost{background:#eef1f7;color:#111}
.grid{display:grid;grid-template-columns:1.1fr .9fr;gap:16px}
.card{background:var(--card);border:1px solid var(--line);border-radius:14px;padding:16px}
.title{font-size:20px;margin:0 0 8px}
.muted{color:var(--muted)}
.row{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-bottom:10px}
select,input,textarea{width:100%;padding:10px;border:1px solid #ced5e1;border-radius:10px;background:#fff;color:#1d2430}
textarea{min-height:180px;resize:vertical}
.actions{display:flex;gap:10px;align-items:center}
.hint{font-size:12px;color:var(--muted)}
.result{margin-top:12px;border:1px solid var(--line);border-radius:10px;background:#fbfcff;padding:12px;white-space:pre-wrap;max-height:360px;overflow:auto}
.error{margin-top:10px;padding:10px;border-radius:10px;background:#fff2f2;color:#9f2d2d;border:1px solid #ffd1d1}
.kv{display:grid;grid-template-columns:120px 1fr auto;gap:8px;align-items:center;margin-bottom:8px}
.mono{font-family:ui-monospace,SFMono-Regular,monospace;font-size:13px;word-break:break-all}
@media (max-width:900px){.grid{grid-template-columns:1fr}.row{grid-template-columns:1fr}}
</style>
</head><body><div class="wrap">
<nav><a href="/admin/providers">Providers</a><a href="/admin/models">Models</a><a href="/admin/keys">Keys</a><a href="/admin/playground">Playground</a><a href="/admin/usage/view">Usage</a><form class="logout" method="post" action="/admin/logout"><button type="submit">Logout</button></form></nav>
<div class="grid">
<section class="card">
<h1 class="title">Playground</h1>
<p class="muted">Use a managed API key to test one message quickly.</p>
<form method="post" action="/admin/playground/chat">
<div class="row">
<div><label>API Key</label><select name="api_key">{{range .Keys}}<option value="{{.Key}}" {{if eq $.PlayAPIKey .Key}}selected{{end}}>{{if .Name}}{{.Name}}{{else}}(unnamed){{end}} / {{.Key}}</option>{{end}}</select></div>
<div><label>Model</label><select name="model">{{range .PlayModels}}<option value="{{.}}" {{if eq $.PlayModel .}}selected{{end}}>{{.}}</option>{{end}}</select></div>
</div>
<div style="margin-bottom:10px"><label>Message</label><textarea name="message" placeholder="Say something...">{{.PlayMessage}}</textarea></div>
<div class="actions"><label><input type="checkbox" name="stream" {{if .PlayStream}}checked{{end}} style="width:auto"> Stream</label><button type="submit">Send</button><a class="hint" href="/admin/playground">Clear</a></div>
</form>
{{if .PlayError}}<div class="error">{{.PlayError}}</div>{{end}}
{{if .PlayResult}}<div class="result">{{.PlayResult}}</div>{{end}}
</section>
<aside class="card">
<h2 class="title">Connect Settings</h2>
<p class="muted">Copy these values into your local client app.</p>
<div class="kv"><div class="muted">API Base URL</div><div class="mono" id="cfg-base">{{.PlayAPIBase}}</div><button type="button" class="ghost" onclick="copyText('cfg-base', this)">Copy</button></div>
<div class="kv"><div class="muted">API Key</div><div class="mono" id="cfg-key">{{.PlayAPIKey}}</div><button type="button" class="ghost" onclick="copyText('cfg-key', this)">Copy</button></div>
<div class="kv"><div class="muted">Model</div><div class="mono" id="cfg-model">{{.PlayModel}}</div><button type="button" class="ghost" onclick="copyText('cfg-model', this)">Copy</button></div>
</aside>
</div>
</div>
<script>
async function copyText(id, button){
  const el = document.getElementById(id);
  if(!el){return;}
  try{
    await navigator.clipboard.writeText(el.textContent || "");
    const origin = button.textContent;
    button.textContent = "Copied";
    setTimeout(()=>{button.textContent = origin;}, 1000);
  }catch(_){
    button.textContent = "Failed";
    setTimeout(()=>{button.textContent = "Copy";}, 1000);
  }
}
</script>
</body></html>`))
