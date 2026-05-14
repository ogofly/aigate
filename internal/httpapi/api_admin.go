package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"aigate/internal/config"
)

type apiProviderResponse struct {
	Name             string `json:"name"`
	BaseURL          string `json:"base_url"`
	AnthropicBaseURL string `json:"anthropic_base_url"`
	AnthropicVersion string `json:"anthropic_version"`
	APIKey           string `json:"api_key,omitempty"`
	APIKeyConfigured bool   `json:"api_key_configured"`
	APIKeyRef        string `json:"api_key_ref"`
	TimeoutSeconds   int    `json:"timeout"`
	Enabled          bool   `json:"enabled"`
}

func writeSuccess(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message})
}

// ---------------------------------------------------------------------------
// Providers API
// ---------------------------------------------------------------------------

// handleApiProvidersList returns all providers.
// GET /api/admin/providers
func (h *Handler) handleApiProvidersList(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	providers, err := h.store.ListProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_providers_list_error", err.Error())
		return
	}
	if providers == nil {
		providers = []config.ProviderConfig{}
	}
	writeJSON(w, http.StatusOK, apiProviderResponses(providers))
}

// handleApiProviderGet returns a single provider by name.
// GET /api/admin/providers/{name}
func (h *Handler) handleApiProviderGet(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	provider, err := h.store.GetProvider(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider_not_found", fmt.Sprintf("provider %q not found", name))
		return
	}
	writeJSON(w, http.StatusOK, apiProviderFromConfig(provider))
}

// handleApiProvidersCreate creates a new provider.
// POST /api/admin/providers
func (h *Handler) handleApiProvidersCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	var body struct {
		Name             string `json:"name"`
		BaseURL          string `json:"base_url"`
		AnthropicBaseURL string `json:"anthropic_base_url"`
		AnthropicVersion string `json:"anthropic_version"`
		APIKey           string `json:"api_key"`
		APIKeyRef        string `json:"api_key_ref"`
		TimeoutSeconds   int    `json:"timeout"`
		Enabled          *bool  `json:"enabled"`
	}
	if !decodeJSONBody(w, r, &body, "invalid JSON body") {
		return
	}

	providerCfg := config.ProviderConfig{
		Name:             strings.TrimSpace(body.Name),
		BaseURL:          strings.TrimSpace(body.BaseURL),
		AnthropicBaseURL: strings.TrimSpace(body.AnthropicBaseURL),
		AnthropicVersion: strings.TrimSpace(body.AnthropicVersion),
		APIKey:           strings.TrimSpace(body.APIKey),
		APIKeyRef:        strings.TrimSpace(body.APIKeyRef),
		TimeoutSeconds:   body.TimeoutSeconds,
		Enabled:          true,
	}
	if body.Enabled != nil {
		providerCfg.Enabled = *body.Enabled
	}

	if err := h.adminService.CreateProvider(r.Context(), providerCfg); err != nil {
		writeAdminError(w, err, http.StatusBadRequest, "api_provider_create_error")
		return
	}
	writeSuccess(w, "provider created")
}

// handleApiProviderUpdate updates an existing provider.
// PUT /api/admin/providers/{name}
func (h *Handler) handleApiProviderUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}

	var body struct {
		BaseURL          string `json:"base_url"`
		AnthropicBaseURL string `json:"anthropic_base_url"`
		AnthropicVersion string `json:"anthropic_version"`
		APIKey           string `json:"api_key"`
		APIKeyRef        string `json:"api_key_ref"`
		TimeoutSeconds   *int   `json:"timeout"`
		Enabled          *bool  `json:"enabled"`
	}
	if !decodeJSONBody(w, r, &body, "invalid JSON body") {
		return
	}

	if err := h.adminService.UpdateProvider(r.Context(), name, ProviderUpdate{
		BaseURL:          body.BaseURL,
		AnthropicBaseURL: body.AnthropicBaseURL,
		AnthropicVersion: body.AnthropicVersion,
		APIKey:           body.APIKey,
		APIKeyRef:        body.APIKeyRef,
		TimeoutSeconds:   body.TimeoutSeconds,
		Enabled:          body.Enabled,
	}); err != nil {
		writeAdminError(w, err, http.StatusBadRequest, "api_provider_update_error")
		return
	}
	writeSuccess(w, "provider updated")
}

// handleApiProviderDelete deletes a provider.
// DELETE /api/admin/providers/{name}
func (h *Handler) handleApiProviderDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}

	if err := h.adminService.DeleteProvider(r.Context(), name); err != nil {
		writeAdminError(w, err, http.StatusInternalServerError, "api_provider_delete_error")
		return
	}
	writeSuccess(w, "provider deleted")
}

// ---------------------------------------------------------------------------
// Models API
// ---------------------------------------------------------------------------

// handleApiModelsList returns all models.
// GET /api/admin/models
func (h *Handler) handleApiModelsList(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	models, err := h.store.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_models_list_error", err.Error())
		return
	}
	if models == nil {
		models = []config.ModelConfig{}
	}
	writeJSON(w, http.StatusOK, models)
}

// handleApiModelsCreate creates a new model.
// POST /api/admin/models
func (h *Handler) handleApiModelsCreate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	var body struct {
		PublicName   string `json:"public_name"`
		Provider     string `json:"provider"`
		UpstreamName string `json:"upstream_name"`
		Priority     int    `json:"priority"`
		Weight       int    `json:"weight"`
		Enabled      *bool  `json:"enabled"`
	}
	if !decodeJSONBody(w, r, &body, "invalid JSON body") {
		return
	}

	modelCfg := config.ModelConfig{
		PublicName:   body.PublicName,
		Provider:     body.Provider,
		UpstreamName: body.UpstreamName,
		Priority:     body.Priority,
		Weight:       body.Weight,
		Enabled:      true,
	}
	if body.Enabled != nil {
		modelCfg.Enabled = *body.Enabled
	}

	if err := h.adminService.CreateModel(r.Context(), modelCfg); err != nil {
		writeAdminError(w, err, http.StatusBadRequest, "api_model_create_error")
		return
	}
	writeSuccess(w, "model created")
}

// handleApiModelUpdate updates an existing model route.
// PUT /api/admin/models/{public_name}
func (h *Handler) handleApiModelUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	identifier := r.PathValue("public_name")
	if identifier == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "model route id is required")
		return
	}

	var body struct {
		PublicName   string `json:"public_name"`
		Provider     string `json:"provider"`
		UpstreamName string `json:"upstream_name"`
		Priority     *int   `json:"priority"`
		Weight       *int   `json:"weight"`
		Enabled      *bool  `json:"enabled"`
	}
	if !decodeJSONBody(w, r, &body, "invalid JSON body") {
		return
	}

	if err := h.adminService.UpdateModelRoute(r.Context(), identifier, ModelUpdate{
		PublicName:   body.PublicName,
		Provider:     body.Provider,
		UpstreamName: body.UpstreamName,
		Priority:     body.Priority,
		Weight:       body.Weight,
		Enabled:      body.Enabled,
	}); err != nil {
		writeAdminError(w, err, http.StatusBadRequest, "api_model_update_error")
		return
	}
	writeSuccess(w, "model updated")
}

// handleApiModelsDelete deletes a model route. The path accepts route id, or
// a public model name for compatibility with older clients.
// DELETE /api/admin/models/{public_name}
func (h *Handler) handleApiModelsDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	publicName := r.PathValue("public_name")
	if publicName == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "public_name is required")
		return
	}

	if err := h.adminService.DeleteModel(r.Context(), publicName); err != nil {
		writeAdminError(w, err, http.StatusBadRequest, "api_model_delete_error")
		return
	}
	writeSuccess(w, "model deleted")
}

// ---------------------------------------------------------------------------
// Keys API
// ---------------------------------------------------------------------------

// handleApiKeysList returns keys visible to the current session.
// GET /api/admin/keys
func (h *Handler) handleApiKeysList(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	keys, err := h.visibleKeys(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_keys_list_error", err.Error())
		return
	}
	if keys == nil {
		keys = []config.KeyConfig{}
	}
	writeJSON(w, http.StatusOK, keys)
}

// handleApiKeysCreate creates a new key.
// POST /api/admin/keys
func (h *Handler) handleApiKeysCreate(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Key           string   `json:"key"`
		Name          string   `json:"name"`
		Owner         string   `json:"owner"`
		Purpose       string   `json:"purpose"`
		ModelAccess   string   `json:"model_access"`
		ModelRouteIDs []string `json:"model_route_ids"`
	}
	if !decodeJSONBody(w, r, &body, "invalid JSON body") {
		return
	}

	key := strings.TrimSpace(body.Key)
	if key == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}

	keyCfg := config.KeyConfig{
		Key:           key,
		Name:          strings.TrimSpace(body.Name),
		Owner:         strings.TrimSpace(body.Owner),
		Purpose:       strings.TrimSpace(body.Purpose),
		ModelAccess:   strings.TrimSpace(body.ModelAccess),
		ModelRouteIDs: cleanRouteIDs(body.ModelRouteIDs),
	}
	if keyCfg.ModelAccess == "" {
		keyCfg.ModelAccess = "all"
	}

	// Non-admin users can only create keys for themselves.
	if session.Role != roleAdmin {
		keyCfg.Owner = session.Username
	}

	if err := h.adminService.CreateAuthKey(r.Context(), keyCfg); err != nil {
		writeAdminError(w, err, http.StatusInternalServerError, "api_key_create_error")
		return
	}
	writeSuccess(w, "key created")
}

// handleApiKeyUpdate updates an existing key.
// PUT /api/admin/keys/{key}
func (h *Handler) handleApiKeyUpdate(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}

	// Verify key exists and check user assignment for non-admin.
	existing, err := h.store.GetAuthKey(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "key not found")
		return
	}
	if session.Role != roleAdmin && existing.Owner != session.Username {
		writeError(w, http.StatusForbidden, "forbidden", "cannot update key of another user")
		return
	}

	var body struct {
		Name          string   `json:"name"`
		Owner         string   `json:"owner"`
		Purpose       string   `json:"purpose"`
		ModelAccess   string   `json:"model_access"`
		ModelRouteIDs []string `json:"model_route_ids"`
	}
	if !decodeJSONBody(w, r, &body, "invalid JSON body") {
		return
	}

	keyCfg := config.KeyConfig{
		Key:           key,
		Name:          strings.TrimSpace(body.Name),
		Owner:         strings.TrimSpace(body.Owner),
		Purpose:       strings.TrimSpace(body.Purpose),
		ModelAccess:   strings.TrimSpace(body.ModelAccess),
		ModelRouteIDs: cleanRouteIDs(body.ModelRouteIDs),
	}
	if keyCfg.ModelAccess == "" {
		keyCfg.ModelAccess = existing.ModelAccess
		keyCfg.ModelRouteIDs = append([]string(nil), existing.ModelRouteIDs...)
	}

	// Non-admin users cannot change the assigned user.
	if session.Role != roleAdmin {
		keyCfg.Owner = existing.Owner
	}

	if err := h.adminService.UpdateAuthKey(r.Context(), keyCfg); err != nil {
		writeAdminError(w, err, http.StatusInternalServerError, "api_key_update_error")
		return
	}
	writeSuccess(w, "key updated")
}

// handleApiKeysDelete deletes a key with user verification.
// DELETE /api/admin/keys/{key}
func (h *Handler) handleApiKeysDelete(w http.ResponseWriter, r *http.Request) {
	session, ok := h.requireWebSession(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}

	// Non-admin can only delete their own keys.
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

	if err := h.adminService.DeleteAuthKey(r.Context(), key); err != nil {
		writeAdminError(w, err, http.StatusInternalServerError, "api_key_delete_error")
		return
	}
	writeSuccess(w, "key deleted")
}

// ---------------------------------------------------------------------------
// Routing Settings API
// ---------------------------------------------------------------------------

// handleApiRoutingSettingsGet returns global routing settings.
// GET /api/admin/routing
func (h *Handler) handleApiRoutingSettingsGet(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	settings, err := h.store.GetRoutingSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_routing_get_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// handleApiRoutingSettingsUpdate updates global routing settings.
// PUT /api/admin/routing
func (h *Handler) handleApiRoutingSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	existing, err := h.store.GetRoutingSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_routing_get_error", err.Error())
		return
	}
	var body struct {
		Selection           string `json:"selection"`
		FailoverEnabled     *bool  `json:"failover_enabled"`
		FailoverMaxAttempts *int   `json:"failover_max_attempts"`
	}
	if !decodeJSONBody(w, r, &body, "invalid JSON body") {
		return
	}
	settings := existing
	if strings.TrimSpace(body.Selection) != "" {
		settings.Selection = strings.TrimSpace(body.Selection)
	}
	if body.FailoverEnabled != nil {
		settings.FailoverEnabled = *body.FailoverEnabled
	}
	if body.FailoverMaxAttempts != nil {
		settings.FailoverMaxAttempts = *body.FailoverMaxAttempts
	}
	if err := h.adminService.UpdateRoutingSettings(r.Context(), settings); err != nil {
		writeAdminError(w, err, http.StatusInternalServerError, "api_routing_update_error")
		return
	}
	writeSuccess(w, "routing settings updated")
}

func apiProviderResponses(providers []config.ProviderConfig) []apiProviderResponse {
	out := make([]apiProviderResponse, 0, len(providers))
	for _, provider := range providers {
		out = append(out, apiProviderFromConfig(provider))
	}
	return out
}

func apiProviderFromConfig(provider config.ProviderConfig) apiProviderResponse {
	return apiProviderResponse{
		Name:             provider.Name,
		BaseURL:          provider.BaseURL,
		AnthropicBaseURL: provider.AnthropicBaseURL,
		AnthropicVersion: provider.AnthropicVersion,
		APIKeyConfigured: strings.TrimSpace(provider.APIKey) != "",
		APIKeyRef:        provider.APIKeyRef,
		TimeoutSeconds:   provider.TimeoutSeconds,
		Enabled:          provider.Enabled,
	}
}

func cleanRouteIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
