package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"llmgate/internal/auth"
	"llmgate/internal/config"
	"llmgate/internal/logger"
	"llmgate/internal/router"
)

const (
	adminReloadTimeout       = 5 * time.Second
	adminStateReloadAttempts = 3
	adminStateReloadDelay    = 50 * time.Millisecond
)

type adminRepository interface {
	ListProviders(ctx context.Context) ([]config.ProviderConfig, error)
	GetProvider(ctx context.Context, name string) (config.ProviderConfig, error)
	UpsertProvider(ctx context.Context, provider config.ProviderConfig) error
	DeleteProvider(ctx context.Context, name string) error
	ListModels(ctx context.Context) ([]config.ModelConfig, error)
	UpsertModel(ctx context.Context, model config.ModelConfig) error
	DeleteModel(ctx context.Context, publicName string) error
	ListAuthKeys(ctx context.Context) ([]config.KeyConfig, error)
	GetAuthKey(ctx context.Context, key string) (config.KeyConfig, error)
	UpsertAuthKey(ctx context.Context, key config.KeyConfig) error
	DeleteAuthKey(ctx context.Context, key string) error
	GetRoutingSettings(ctx context.Context) (config.RoutingConfig, error)
	UpsertRoutingSettings(ctx context.Context, settings config.RoutingConfig) error
}

type adminRouterState interface {
	Update(models []config.ModelConfig, providers []config.ProviderConfig, settings config.RoutingConfig) error
}

type adminAuthState interface {
	Update(keys []config.KeyConfig)
}

type AdminService struct {
	repo             adminRepository
	router           adminRouterState
	auth             adminAuthState
	setProviderNames func([]string)
	reloadAttempts   int
	reloadDelay      time.Duration
}

type AdminError struct {
	Status  int
	Code    string
	Message string
	Err     error
}

func (e *AdminError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *AdminError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewAdminService(repo adminRepository, rt *router.Router, authenticator *auth.Auth, setProviderNames func([]string)) *AdminService {
	return &AdminService{
		repo:             repo,
		router:           rt,
		auth:             authenticator,
		setProviderNames: setProviderNames,
		reloadAttempts:   adminStateReloadAttempts,
		reloadDelay:      adminStateReloadDelay,
	}
}

func (s *AdminService) CreateProvider(ctx context.Context, provider config.ProviderConfig) error {
	if err := provider.Validate(); err != nil {
		return adminError(http.StatusBadRequest, "invalid_request", err.Error(), err)
	}
	if err := s.repo.UpsertProvider(ctx, provider); err != nil {
		return adminError(http.StatusBadRequest, "api_provider_create_error", err.Error(), err)
	}
	if err := s.RefreshProvidersAndModels(ctx); err != nil {
		return adminError(http.StatusInternalServerError, "api_provider_reload_error", "provider saved but runtime state reload failed", err)
	}
	return nil
}

func (s *AdminService) UpdateProvider(ctx context.Context, name string, update ProviderUpdate) error {
	if strings.TrimSpace(name) == "" {
		return adminError(http.StatusBadRequest, "invalid_request", "name is required", nil)
	}
	existing, err := s.repo.GetProvider(ctx, name)
	if err != nil {
		return adminError(http.StatusNotFound, "provider_not_found", fmt.Sprintf("provider %q not found", name), err)
	}
	baseURL := strings.TrimSpace(update.BaseURL)
	if baseURL == "" {
		return adminError(http.StatusBadRequest, "invalid_request", "base_url is required", nil)
	}
	apiKey := strings.TrimSpace(update.APIKey)
	apiKeyRef := strings.TrimSpace(update.APIKeyRef)
	if !update.APIKeySet {
		apiKey = existing.APIKey
	}
	if !update.APIKeyRefSet {
		apiKeyRef = existing.APIKeyRef
	}
	if update.APIKeySet && apiKey == "" {
		apiKey = existing.APIKey
	}
	timeoutSeconds := existing.TimeoutSeconds
	if update.TimeoutSeconds != nil {
		timeoutSeconds = *update.TimeoutSeconds
	}
	enabled := existing.Enabled
	if update.Enabled != nil {
		enabled = *update.Enabled
	}
	provider := config.ProviderConfig{
		Name:             name,
		BaseURL:          baseURL,
		AnthropicBaseURL: strings.TrimSpace(update.AnthropicBaseURL),
		AnthropicVersion: strings.TrimSpace(update.AnthropicVersion),
		APIKey:           apiKey,
		APIKeyRef:        apiKeyRef,
		TimeoutSeconds:   timeoutSeconds,
		Enabled:          enabled,
	}
	if err := provider.Validate(); err != nil {
		return adminError(http.StatusBadRequest, "invalid_request", err.Error(), err)
	}
	if err := s.repo.UpsertProvider(ctx, provider); err != nil {
		return adminError(http.StatusBadRequest, "api_provider_update_error", err.Error(), err)
	}
	if err := s.RefreshProvidersAndModels(ctx); err != nil {
		return adminError(http.StatusInternalServerError, "api_provider_reload_error", "provider updated but runtime state reload failed", err)
	}
	return nil
}

func (s *AdminService) DeleteProvider(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return adminError(http.StatusBadRequest, "invalid_request", "name is required", nil)
	}
	models, err := s.repo.ListModels(ctx)
	if err != nil {
		return adminError(http.StatusInternalServerError, "api_provider_delete_error", err.Error(), err)
	}
	for _, model := range models {
		if model.Provider == name {
			return adminError(http.StatusBadRequest, "api_provider_delete_error", "provider is still used by models", nil)
		}
	}
	if err := s.repo.DeleteProvider(ctx, name); err != nil {
		return adminError(http.StatusInternalServerError, "api_provider_delete_error", err.Error(), err)
	}
	if err := s.RefreshProvidersAndModels(ctx); err != nil {
		return adminError(http.StatusInternalServerError, "api_provider_reload_error", "provider deleted but runtime state reload failed", err)
	}
	return nil
}

func (s *AdminService) CreateModel(ctx context.Context, model config.ModelConfig) error {
	model.PublicName = strings.TrimSpace(model.PublicName)
	model.Provider = strings.TrimSpace(model.Provider)
	model.UpstreamName = strings.TrimSpace(model.UpstreamName)
	if model.PublicName == "" || model.Provider == "" || model.UpstreamName == "" {
		return adminError(http.StatusBadRequest, "invalid_request", "public_name, provider, upstream_name are required", nil)
	}
	if ok, err := s.providerExists(ctx, model.Provider); err != nil {
		return adminError(http.StatusInternalServerError, "api_model_create_error", err.Error(), err)
	} else if !ok {
		return adminError(http.StatusBadRequest, "invalid_request", "provider not found", nil)
	}
	if err := s.repo.UpsertModel(ctx, model); err != nil {
		return adminError(http.StatusBadRequest, "api_model_create_error", err.Error(), err)
	}
	if err := s.RefreshModels(ctx); err != nil {
		return adminError(http.StatusInternalServerError, "api_model_reload_error", "model saved but runtime state reload failed", err)
	}
	return nil
}

func (s *AdminService) UpdateModelRoute(ctx context.Context, identifier string, patch ModelUpdate) error {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return adminError(http.StatusBadRequest, "invalid_request", "model route id is required", nil)
	}
	models, err := s.repo.ListModels(ctx)
	if err != nil {
		return adminError(http.StatusInternalServerError, "api_model_update_error", err.Error(), err)
	}
	existing, err := resolveModelRoute(identifier, models)
	if err != nil {
		if errors.Is(err, errAmbiguousModelRoute) {
			return adminError(http.StatusBadRequest, "ambiguous_model_route", "model has multiple routes; use model route id", err)
		}
		return adminError(http.StatusNotFound, "model_not_found", fmt.Sprintf("model %q not found", identifier), err)
	}
	provider := strings.TrimSpace(patch.Provider)
	if provider == "" {
		provider = existing.Provider
	}
	if ok, err := s.providerExists(ctx, provider); err != nil {
		return adminError(http.StatusInternalServerError, "api_model_update_error", err.Error(), err)
	} else if !ok {
		return adminError(http.StatusBadRequest, "invalid_request", "provider not found", nil)
	}
	upstreamName := strings.TrimSpace(patch.UpstreamName)
	if upstreamName == "" {
		upstreamName = existing.UpstreamName
	}
	publicName := strings.TrimSpace(patch.PublicName)
	if publicName == "" {
		publicName = existing.PublicName
	}
	priority := existing.Priority
	if patch.Priority != nil {
		priority = *patch.Priority
	}
	weight := existing.Weight
	if patch.Weight != nil {
		weight = *patch.Weight
	}
	enabled := existing.Enabled
	if patch.Enabled != nil {
		enabled = *patch.Enabled
	}
	model := config.ModelConfig{
		ID:           existing.ID,
		PublicName:   publicName,
		Provider:     provider,
		UpstreamName: upstreamName,
		Priority:     priority,
		Weight:       weight,
		Enabled:      enabled,
	}
	if err := s.repo.UpsertModel(ctx, model); err != nil {
		return adminError(http.StatusBadRequest, "api_model_update_error", err.Error(), err)
	}
	if err := s.RefreshModels(ctx); err != nil {
		return adminError(http.StatusInternalServerError, "api_model_reload_error", "model updated but runtime state reload failed", err)
	}
	return nil
}

func (s *AdminService) DeleteModel(ctx context.Context, identifier string) error {
	if strings.TrimSpace(identifier) == "" {
		return adminError(http.StatusBadRequest, "invalid_request", "model route id is required", nil)
	}
	if err := s.repo.DeleteModel(ctx, identifier); err != nil {
		return adminError(http.StatusBadRequest, "api_model_delete_error", err.Error(), err)
	}
	if err := s.RefreshModels(ctx); err != nil {
		return adminError(http.StatusInternalServerError, "api_model_reload_error", "model deleted but runtime state reload failed", err)
	}
	return nil
}

func (s *AdminService) CreateAuthKey(ctx context.Context, key config.KeyConfig) error {
	if strings.TrimSpace(key.Key) == "" {
		return adminError(http.StatusBadRequest, "invalid_request", "key is required", nil)
	}
	if _, err := s.repo.GetAuthKey(ctx, key.Key); err == nil {
		return adminError(http.StatusConflict, "api_key_duplicate", "key already exists", nil)
	}
	if err := s.repo.UpsertAuthKey(ctx, key); err != nil {
		return adminError(http.StatusInternalServerError, "api_key_create_error", err.Error(), err)
	}
	if err := s.RefreshAuthKeys(ctx); err != nil {
		return adminError(http.StatusInternalServerError, "api_key_reload_error", "key saved but runtime auth reload failed", err)
	}
	return nil
}

func (s *AdminService) UpdateAuthKey(ctx context.Context, key config.KeyConfig) error {
	if strings.TrimSpace(key.Key) == "" {
		return adminError(http.StatusBadRequest, "invalid_request", "key is required", nil)
	}
	if err := s.repo.UpsertAuthKey(ctx, key); err != nil {
		return adminError(http.StatusInternalServerError, "api_key_update_error", err.Error(), err)
	}
	if err := s.RefreshAuthKeys(ctx); err != nil {
		return adminError(http.StatusInternalServerError, "api_key_reload_error", "key updated but runtime auth reload failed", err)
	}
	return nil
}

func (s *AdminService) DeleteAuthKey(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return adminError(http.StatusBadRequest, "invalid_request", "key is required", nil)
	}
	if err := s.repo.DeleteAuthKey(ctx, key); err != nil {
		return adminError(http.StatusInternalServerError, "api_key_delete_error", err.Error(), err)
	}
	if err := s.RefreshAuthKeys(ctx); err != nil {
		return adminError(http.StatusInternalServerError, "api_key_reload_error", "key deleted but runtime auth reload failed", err)
	}
	return nil
}

func (s *AdminService) UpdateRoutingSettings(ctx context.Context, settings config.RoutingConfig) error {
	if settings.Selection != "priority" && settings.Selection != "weight" && settings.Selection != "random" {
		return adminError(http.StatusBadRequest, "invalid_request", "selection must be priority, weight, or random", nil)
	}
	if settings.FailoverMaxAttempts <= 0 {
		return adminError(http.StatusBadRequest, "invalid_request", "failover_max_attempts must be greater than 0", nil)
	}
	if err := s.repo.UpsertRoutingSettings(ctx, settings); err != nil {
		return adminError(http.StatusInternalServerError, "api_routing_update_error", err.Error(), err)
	}
	if err := s.RefreshModels(ctx); err != nil {
		return adminError(http.StatusInternalServerError, "api_routing_reload_error", "routing settings saved but runtime state reload failed", err)
	}
	return nil
}

func (s *AdminService) RefreshModels(ctx context.Context) error {
	return s.withReloadRetry(ctx, "models", func(ctx context.Context) error {
		models, err := s.repo.ListModels(ctx)
		if err != nil {
			return err
		}
		providers, err := s.repo.ListProviders(ctx)
		if err != nil {
			return err
		}
		settings, err := s.repo.GetRoutingSettings(ctx)
		if err != nil {
			return err
		}
		return s.router.Update(models, providers, settings)
	})
}

func (s *AdminService) RefreshProvidersAndModels(ctx context.Context) error {
	return s.withReloadRetry(ctx, "providers_and_models", func(ctx context.Context) error {
		models, err := s.repo.ListModels(ctx)
		if err != nil {
			return err
		}
		providers, err := s.repo.ListProviders(ctx)
		if err != nil {
			return err
		}
		settings, err := s.repo.GetRoutingSettings(ctx)
		if err != nil {
			return err
		}
		if err := s.router.Update(models, providers, settings); err != nil {
			return err
		}
		if s.setProviderNames != nil {
			names := make([]string, 0, len(providers))
			for _, provider := range providers {
				names = append(names, provider.Name)
			}
			s.setProviderNames(names)
		}
		return nil
	})
}

func (s *AdminService) RefreshAuthKeys(ctx context.Context) error {
	return s.withReloadRetry(ctx, "auth_keys", func(ctx context.Context) error {
		keys, err := s.repo.ListAuthKeys(ctx)
		if err != nil {
			return err
		}
		s.auth.Update(keys)
		return nil
	})
}

func (s *AdminService) providerExists(ctx context.Context, name string) (bool, error) {
	providers, err := s.repo.ListProviders(ctx)
	if err != nil {
		return false, err
	}
	for _, provider := range providers {
		if provider.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (s *AdminService) withReloadRetry(_ context.Context, label string, fn func(context.Context) error) error {
	attempts := s.reloadAttempts
	if attempts <= 0 {
		attempts = 1
	}
	delay := s.reloadDelay
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), adminReloadTimeout)
		err := fn(ctx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		logger.L.Warn("admin state reload failed", "target", label, "attempt", attempt, "attempts", attempts, "error", err)
		if attempt < attempts && delay > 0 {
			time.Sleep(delay)
		}
	}
	return lastErr
}

func adminError(status int, code, message string, err error) *AdminError {
	return &AdminError{Status: status, Code: code, Message: message, Err: err}
}

func writeAdminError(w http.ResponseWriter, err error, fallbackStatus int, fallbackCode string) {
	var adminErr *AdminError
	if errors.As(err, &adminErr) {
		writeError(w, adminErr.Status, adminErr.Code, adminErr.Message)
		return
	}
	writeError(w, fallbackStatus, fallbackCode, err.Error())
}

type ProviderUpdate struct {
	BaseURL          string
	AnthropicBaseURL string
	AnthropicVersion string
	APIKey           string
	APIKeySet        bool
	APIKeyRef        string
	APIKeyRefSet     bool
	TimeoutSeconds   *int
	Enabled          *bool
}

type ModelUpdate struct {
	PublicName   string
	Provider     string
	UpstreamName string
	Priority     *int
	Weight       *int
	Enabled      *bool
}

var errAmbiguousModelRoute = errors.New("ambiguous model route")
var errModelRouteNotFound = errors.New("model route not found")

func resolveModelRoute(identifier string, models []config.ModelConfig) (config.ModelConfig, error) {
	var found config.ModelConfig
	foundByPublicName := false
	publicNameMatches := 0
	for _, model := range models {
		if model.ID == identifier {
			return model, nil
		}
		if model.PublicName == identifier {
			publicNameMatches++
			found = model
			foundByPublicName = true
		}
	}
	if publicNameMatches > 1 {
		return config.ModelConfig{}, errAmbiguousModelRoute
	}
	if foundByPublicName {
		return found, nil
	}
	return config.ModelConfig{}, errModelRouteNotFound
}
