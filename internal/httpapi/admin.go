package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"

	"aigate/internal/config"
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
<style>body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:480px;margin:48px auto;padding:0 16px;color:#222}input{width:100%;padding:10px;margin:8px 0;border:1px solid #ccc;border-radius:8px}button{padding:10px 16px;border:0;background:#111;color:#fff;border-radius:8px} .error{color:#b00020;margin-bottom:12px}</style>
</head><body><h1>Admin Login</h1>{{if .Error}}<div class="error">{{.Error}}</div>{{end}}<form method="post" action="/admin/login"><input name="username" placeholder="username"><input type="password" name="password" placeholder="password"><button type="submit">Sign In</button></form></body></html>`))

var adminProvidersTemplate = template.Must(template.New("providers").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:1300px;margin:32px auto;padding:0 16px;color:#222}nav a{margin-right:16px;color:#111;text-decoration:none}table{width:100%;border-collapse:collapse;margin-top:24px}th,td{padding:10px;border-bottom:1px solid #eee;text-align:left}input{padding:10px;border:1px solid #ccc;border-radius:8px;margin-right:8px;min-width:140px}button{padding:8px 12px;border:0;background:#111;color:#fff;border-radius:8px}.muted{color:#666}.flash{padding:10px 12px;background:#edf7ed;border-radius:8px;margin-top:16px}.mono{font-family:ui-monospace,SFMono-Regular,monospace}</style>
</head><body><nav><a href="/admin/providers">Providers</a><a href="/admin/models">Models</a><a href="/admin/keys">Keys</a><a href="/admin/usage/view">Usage</a><form style="display:inline" method="post" action="/admin/logout"><button type="submit">Logout</button></form></nav><h1>Providers</h1>{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}<form method="post" action="/admin/providers" style="margin-top:20px"><input name="name" placeholder="name"><input name="base_url" placeholder="base url"><input name="api_key" placeholder="api key (optional)"><input name="api_key_ref" placeholder="env var name (optional)"><input name="timeout" placeholder="timeout seconds" value="60"><button type="submit">Save</button></form><div class="muted" style="margin-top:8px">Fill either <span class="mono">api_key</span> or <span class="mono">api_key_ref</span>.</div><table><thead><tr><th>Name</th><th>Base URL</th><th>API Key</th><th>Secret Ref</th><th>Timeout</th><th></th></tr></thead><tbody>{{range .ProvidersCfg}}<tr><td>{{.Name}}</td><td>{{.BaseURL}}</td><td>{{if .APIKey}}configured{{else}}-{{end}}</td><td>{{if .APIKeyRef}}{{.APIKeyRef}}{{else}}-{{end}}</td><td>{{.TimeoutSeconds}}s</td><td><form method="post" action="/admin/providers/delete"><input type="hidden" name="name" value="{{.Name}}"><button type="submit">Delete</button></form></td></tr>{{else}}<tr><td colspan="6" class="muted">No providers</td></tr>{{end}}</tbody></table></body></html>`))

var adminModelsTemplate = template.Must(template.New("models").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:1100px;margin:32px auto;padding:0 16px;color:#222}nav a{margin-right:16px;color:#111;text-decoration:none}table{width:100%;border-collapse:collapse;margin-top:24px}th,td{padding:10px;border-bottom:1px solid #eee;text-align:left}input,select{padding:10px;border:1px solid #ccc;border-radius:8px;margin-right:8px}button{padding:8px 12px;border:0;background:#111;color:#fff;border-radius:8px}.muted{color:#666}.flash{padding:10px 12px;background:#edf7ed;border-radius:8px;margin-top:16px}</style>
</head><body><nav><a href="/admin/providers">Providers</a><a href="/admin/models">Models</a><a href="/admin/keys">Keys</a><a href="/admin/usage/view">Usage</a><form style="display:inline" method="post" action="/admin/logout"><button type="submit">Logout</button></form></nav><h1>Models</h1>{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}<form method="post" action="/admin/models" style="margin-top:20px"><input name="public_name" placeholder="public name"><select name="provider">{{range .Providers}}<option value="{{.}}">{{.}}</option>{{end}}</select><input name="upstream_name" placeholder="upstream name"><button type="submit">Save</button></form><table><thead><tr><th>Public</th><th>Provider</th><th>Upstream</th><th></th></tr></thead><tbody>{{range .Models}}<tr><td>{{.PublicName}}</td><td>{{.Provider}}</td><td>{{.UpstreamName}}</td><td><form method="post" action="/admin/models/delete"><input type="hidden" name="public_name" value="{{.PublicName}}"><button type="submit">Delete</button></form></td></tr>{{else}}<tr><td colspan="4" class="muted">No models</td></tr>{{end}}</tbody></table></body></html>`))

var adminKeysTemplate = template.Must(template.New("keys").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:1200px;margin:32px auto;padding:0 16px;color:#222}nav a{margin-right:16px;color:#111;text-decoration:none}table{width:100%;border-collapse:collapse;margin-top:24px}th,td{padding:10px;border-bottom:1px solid #eee;text-align:left}input{padding:10px;border:1px solid #ccc;border-radius:8px;margin-right:8px;min-width:120px}label{margin-right:12px}button{padding:8px 12px;border:0;background:#111;color:#fff;border-radius:8px}.muted{color:#666}.flash{padding:10px 12px;background:#edf7ed;border-radius:8px;margin-top:16px}.key-cell{display:flex;align-items:center;gap:8px}.key-text{font-family:ui-monospace,SFMono-Regular,monospace}.ghost{background:#f3f3f3;color:#111}</style>
</head><body><nav><a href="/admin/providers">Providers</a><a href="/admin/models">Models</a><a href="/admin/keys">Keys</a><a href="/admin/usage/view">Usage</a><form style="display:inline" method="post" action="/admin/logout"><button type="submit">Logout</button></form></nav><h1>Keys</h1>{{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}<form method="post" action="/admin/keys" style="margin-top:20px"><input name="key" placeholder="api key"><input name="name" placeholder="name"><input name="owner" placeholder="owner"><input name="purpose" placeholder="purpose"><button type="submit">Save</button></form><table><thead><tr><th>Name</th><th>Owner</th><th>Purpose</th><th>Key</th><th></th></tr></thead><tbody>{{range .Keys}}<tr><td>{{.Name}}</td><td>{{.Owner}}</td><td>{{.Purpose}}</td><td><div class="key-cell"><span class="key-text" data-key="{{.Key}}">****</span><button type="button" class="ghost" onclick="toggleKey(this)">Show</button><button type="button" class="ghost" onclick="copyKey(this)">Copy</button></div></td><td><form method="post" action="/admin/keys/delete"><input type="hidden" name="key" value="{{.Key}}"><button type="submit">Delete</button></form></td></tr>{{else}}<tr><td colspan="5" class="muted">No keys</td></tr>{{end}}</tbody></table><script>function toggleKey(button){const text=button.parentNode.querySelector('.key-text');const hidden=text.textContent==='****';text.textContent=hidden?text.dataset.key:'****';button.textContent=hidden?'Hide':'Show';}async function copyKey(button){const text=button.parentNode.querySelector('.key-text');const value=text.dataset.key;try{await navigator.clipboard.writeText(value);button.textContent='Copied';setTimeout(()=>{button.textContent='Copy';},1200);}catch(e){button.textContent='Copy failed';setTimeout(()=>{button.textContent='Copy';},1200);}}</script></body></html>`))

var adminUsageTemplate = template.Must(template.New("usage").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;max-width:1100px;margin:32px auto;padding:0 16px;color:#222}nav a{margin-right:16px;color:#111;text-decoration:none}table{width:100%;border-collapse:collapse;margin-top:24px}th,td{padding:10px;border-bottom:1px solid #eee;text-align:left}button{padding:8px 12px;border:0;background:#111;color:#fff;border-radius:8px}.muted{color:#666}</style>
</head><body><nav><a href="/admin/providers">Providers</a><a href="/admin/models">Models</a><a href="/admin/keys">Keys</a><a href="/admin/usage/view">Usage</a><form style="display:inline" method="post" action="/admin/logout"><button type="submit">Logout</button></form></nav><h1>Usage</h1><table><thead><tr><th>Key ID</th><th>Name</th><th>Owner</th><th>Purpose</th><th>Requests</th><th>Success</th><th>Errors</th><th>Total Tokens</th></tr></thead><tbody>{{range .Usage}}<tr><td>{{.KeyID}}</td><td>{{.KeyName}}</td><td>{{.Owner}}</td><td>{{.Purpose}}</td><td>{{.RequestCount}}</td><td>{{.SuccessCount}}</td><td>{{.ErrorCount}}</td><td>{{.TotalTokens}}</td></tr>{{else}}<tr><td colspan="8" class="muted">No usage yet</td></tr>{{end}}</tbody></table></body></html>`))
