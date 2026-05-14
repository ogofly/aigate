package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"aigate/internal/config"
)

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
	writeJSON(w, http.StatusOK, providers)
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
	writeJSON(w, http.StatusOK, provider)
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
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
	}

	if err := providerCfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := h.store.UpsertProvider(r.Context(), providerCfg); err != nil {
		writeError(w, http.StatusBadRequest, "api_provider_create_error", err.Error())
		return
	}
	if err := h.reloadProvidersAndModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "api_provider_reload_error", err.Error())
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

	existing, err := h.store.GetProvider(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider_not_found", fmt.Sprintf("provider %q not found", name))
		return
	}

	var body struct {
		BaseURL          string `json:"base_url"`
		AnthropicBaseURL string `json:"anthropic_base_url"`
		AnthropicVersion string `json:"anthropic_version"`
		APIKey           string `json:"api_key"`
		APIKeyRef        string `json:"api_key_ref"`
		TimeoutSeconds   *int   `json:"timeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	baseURL := strings.TrimSpace(body.BaseURL)
	if baseURL == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "base_url is required")
		return
	}

	apiKey := strings.TrimSpace(body.APIKey)
	apiKeyRef := strings.TrimSpace(body.APIKeyRef)
	if apiKey == "" && apiKeyRef == "" {
		apiKey = existing.APIKey
		apiKeyRef = existing.APIKeyRef
	}

	timeoutSeconds := existing.TimeoutSeconds
	if body.TimeoutSeconds != nil {
		timeoutSeconds = *body.TimeoutSeconds
	}

	providerCfg := config.ProviderConfig{
		Name:             name,
		BaseURL:          baseURL,
		AnthropicBaseURL: strings.TrimSpace(body.AnthropicBaseURL),
		AnthropicVersion: strings.TrimSpace(body.AnthropicVersion),
		APIKey:           apiKey,
		APIKeyRef:        apiKeyRef,
		TimeoutSeconds:   timeoutSeconds,
	}

	if err := providerCfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := h.store.UpsertProvider(r.Context(), providerCfg); err != nil {
		writeError(w, http.StatusBadRequest, "api_provider_update_error", err.Error())
		return
	}
	if err := h.reloadProvidersAndModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "api_provider_reload_error", err.Error())
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

	// Check if any model still references this provider.
	models, err := h.store.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_provider_delete_error", err.Error())
		return
	}
	for _, model := range models {
		if model.Provider == name {
			writeError(w, http.StatusBadRequest, "api_provider_delete_error", "provider is still used by models")
			return
		}
	}

	if err := h.store.DeleteProvider(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, "api_provider_delete_error", err.Error())
		return
	}
	if err := h.reloadProvidersAndModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "api_provider_reload_error", err.Error())
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
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	publicName := strings.TrimSpace(body.PublicName)
	provider := strings.TrimSpace(body.Provider)
	upstreamName := strings.TrimSpace(body.UpstreamName)

	if publicName == "" || provider == "" || upstreamName == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "public_name, provider, upstream_name are required")
		return
	}

	// Verify provider exists.
	if !containsString(h.listProviderNames(), provider) {
		writeError(w, http.StatusBadRequest, "invalid_request", "provider not found")
		return
	}

	modelCfg := config.ModelConfig{
		PublicName:   publicName,
		Provider:     provider,
		UpstreamName: upstreamName,
	}

	if err := h.store.UpsertModel(r.Context(), modelCfg); err != nil {
		writeError(w, http.StatusBadRequest, "api_model_create_error", err.Error())
		return
	}
	if err := h.reloadModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "api_model_reload_error", err.Error())
		return
	}
	writeSuccess(w, "model created")
}

// handleApiModelUpdate updates an existing model.
// PUT /api/admin/models/{public_name}
func (h *Handler) handleApiModelUpdate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminSession(w, r) {
		return
	}
	publicName := r.PathValue("public_name")
	if publicName == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "public_name is required")
		return
	}

	models, err := h.store.ListModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_model_update_error", err.Error())
		return
	}
	foundExisting := false
	for _, model := range models {
		if model.PublicName == publicName {
			foundExisting = true
			break
		}
	}
	if !foundExisting {
		writeError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("model %q not found", publicName))
		return
	}

	var body struct {
		PublicName   string `json:"public_name"`
		Provider     string `json:"provider"`
		UpstreamName string `json:"upstream_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	provider := strings.TrimSpace(body.Provider)
	upstreamName := strings.TrimSpace(body.UpstreamName)
	if provider == "" || upstreamName == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "provider and upstream_name are required")
		return
	}

	// Verify provider exists.
	if !containsString(h.listProviderNames(), provider) {
		writeError(w, http.StatusBadRequest, "invalid_request", "provider not found")
		return
	}

	// Use new public_name from body if provided, otherwise keep the path value.
	newPublicName := strings.TrimSpace(body.PublicName)
	if newPublicName == "" {
		newPublicName = publicName
	}
	if newPublicName != publicName {
		for _, model := range models {
			if model.PublicName == newPublicName {
				writeError(w, http.StatusConflict, "api_model_duplicate", fmt.Sprintf("model %q already exists", newPublicName))
				return
			}
		}
	}

	modelCfg := config.ModelConfig{
		PublicName:   newPublicName,
		Provider:     provider,
		UpstreamName: upstreamName,
	}

	if err := h.store.UpsertModel(r.Context(), modelCfg); err != nil {
		writeError(w, http.StatusBadRequest, "api_model_update_error", err.Error())
		return
	}
	if newPublicName != publicName {
		if err := h.store.DeleteModel(r.Context(), publicName); err != nil {
			writeError(w, http.StatusInternalServerError, "api_model_update_error", err.Error())
			return
		}
	}
	if err := h.reloadModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "api_model_reload_error", err.Error())
		return
	}
	writeSuccess(w, "model updated")
}

// handleApiModelsDelete deletes a model.
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

	if err := h.store.DeleteModel(r.Context(), publicName); err != nil {
		writeError(w, http.StatusInternalServerError, "api_model_delete_error", err.Error())
		return
	}
	if err := h.reloadModels(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "api_model_reload_error", err.Error())
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
		Key     string `json:"key"`
		Name    string `json:"name"`
		Owner   string `json:"owner"`
		Purpose string `json:"purpose"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	key := strings.TrimSpace(body.Key)
	if key == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}

	// Check if key already exists.
	if _, err := h.store.GetAuthKey(r.Context(), key); err == nil {
		writeError(w, http.StatusConflict, "api_key_duplicate", "key already exists")
		return
	}

	keyCfg := config.KeyConfig{
		Key:     key,
		Name:    strings.TrimSpace(body.Name),
		Owner:   strings.TrimSpace(body.Owner),
		Purpose: strings.TrimSpace(body.Purpose),
	}

	// Non-admin users can only create keys for themselves.
	if session.Role != roleAdmin {
		keyCfg.Owner = session.Username
	}

	if err := h.store.UpsertAuthKey(r.Context(), keyCfg); err != nil {
		writeError(w, http.StatusInternalServerError, "api_key_create_error", err.Error())
		return
	}
	if err := h.reloadAuthKeys(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "api_key_reload_error", err.Error())
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

	// Verify key exists and check ownership for non-admin.
	existing, err := h.store.GetAuthKey(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "key not found")
		return
	}
	if session.Role != roleAdmin && existing.Owner != session.Username {
		writeError(w, http.StatusForbidden, "forbidden", "cannot update key of another owner")
		return
	}

	var body struct {
		Name    string `json:"name"`
		Owner   string `json:"owner"`
		Purpose string `json:"purpose"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	keyCfg := config.KeyConfig{
		Key:     key,
		Name:    strings.TrimSpace(body.Name),
		Owner:   strings.TrimSpace(body.Owner),
		Purpose: strings.TrimSpace(body.Purpose),
	}

	// Non-admin users cannot change the owner.
	if session.Role != roleAdmin {
		keyCfg.Owner = existing.Owner
	}

	if err := h.store.UpsertAuthKey(r.Context(), keyCfg); err != nil {
		writeError(w, http.StatusInternalServerError, "api_key_update_error", err.Error())
		return
	}
	if err := h.reloadAuthKeys(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "api_key_reload_error", err.Error())
		return
	}
	writeSuccess(w, "key updated")
}

// handleApiKeysDelete deletes a key with owner verification.
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
			writeError(w, http.StatusForbidden, "forbidden", "cannot delete key of another owner")
			return
		}
	}

	if err := h.store.DeleteAuthKey(r.Context(), key); err != nil {
		writeError(w, http.StatusInternalServerError, "api_key_delete_error", err.Error())
		return
	}
	if err := h.reloadAuthKeys(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "api_key_reload_error", err.Error())
		return
	}
	writeSuccess(w, "key deleted")
}
