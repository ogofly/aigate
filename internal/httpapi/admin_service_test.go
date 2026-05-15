package httpapi

import (
	"context"
	"errors"
	"testing"

	"aigate/internal/auth"
	"aigate/internal/config"
)

type adminServiceRepoStub struct {
	providers       []config.ProviderConfig
	models          []config.ModelConfig
	keys            []config.KeyConfig
	settings        config.RoutingConfig
	listModelsError []error
	upsertedModels  []config.ModelConfig
}

func (r *adminServiceRepoStub) ListProviders(context.Context) ([]config.ProviderConfig, error) {
	return append([]config.ProviderConfig(nil), r.providers...), nil
}

func (r *adminServiceRepoStub) GetProvider(_ context.Context, name string) (config.ProviderConfig, error) {
	for _, provider := range r.providers {
		if provider.Name == name {
			return provider, nil
		}
	}
	return config.ProviderConfig{}, errors.New("provider not found")
}

func (r *adminServiceRepoStub) UpsertProvider(_ context.Context, provider config.ProviderConfig) error {
	r.providers = append(r.providers, provider)
	return nil
}

func (r *adminServiceRepoStub) DeleteProvider(context.Context, string) error { return nil }

func (r *adminServiceRepoStub) ListModels(context.Context) ([]config.ModelConfig, error) {
	if len(r.listModelsError) > 0 {
		err := r.listModelsError[0]
		r.listModelsError = r.listModelsError[1:]
		if err != nil {
			return nil, err
		}
	}
	return append([]config.ModelConfig(nil), r.models...), nil
}

func (r *adminServiceRepoStub) UpsertModel(_ context.Context, model config.ModelConfig) error {
	r.upsertedModels = append(r.upsertedModels, model)
	r.models = append(r.models, model)
	return nil
}

func (r *adminServiceRepoStub) DeleteModel(context.Context, string) error { return nil }

func (r *adminServiceRepoStub) ListAuthKeys(context.Context) ([]config.KeyConfig, error) {
	return append([]config.KeyConfig(nil), r.keys...), nil
}

func (r *adminServiceRepoStub) GetAuthKey(_ context.Context, key string) (config.KeyConfig, error) {
	for _, item := range r.keys {
		if item.Key == key {
			return item, nil
		}
	}
	return config.KeyConfig{}, errors.New("key not found")
}

func (r *adminServiceRepoStub) UpsertAuthKey(_ context.Context, key config.KeyConfig) error {
	r.keys = append(r.keys, key)
	return nil
}

func (r *adminServiceRepoStub) DeleteAuthKey(context.Context, string) error { return nil }

func (r *adminServiceRepoStub) GetRoutingSettings(context.Context) (config.RoutingConfig, error) {
	return r.settings, nil
}

func (r *adminServiceRepoStub) UpsertRoutingSettings(_ context.Context, settings config.RoutingConfig) error {
	r.settings = settings
	return nil
}

type adminServiceRouterStub struct {
	updateCount int
}

func (r *adminServiceRouterStub) Update([]config.ModelConfig, []config.ProviderConfig, config.RoutingConfig) error {
	r.updateCount++
	return nil
}

func TestAdminServiceRetriesRuntimeReloadAfterMutation(t *testing.T) {
	repo := &adminServiceRepoStub{
		providers: []config.ProviderConfig{{
			Name:    "openai",
			BaseURL: "https://api.openai.test/v1",
			APIKey:  "secret",
			Enabled: true,
		}},
		settings:        config.RoutingConfig{Selection: "priority", FailoverEnabled: true, FailoverMaxAttempts: 2},
		listModelsError: []error{errors.New("temporary read failure"), nil},
	}
	rt := &adminServiceRouterStub{}
	svc := &AdminService{
		repo:           repo,
		router:         rt,
		auth:           auth.New(nil),
		reloadAttempts: 2,
	}

	err := svc.CreateModel(context.Background(), config.ModelConfig{
		PublicName:   "gpt-4o",
		Provider:     "openai",
		UpstreamName: "gpt-4o",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateModel() error = %v", err)
	}
	if len(repo.upsertedModels) != 1 {
		t.Fatalf("upserted models = %d, want 1", len(repo.upsertedModels))
	}
	if rt.updateCount != 1 {
		t.Fatalf("router updates = %d, want 1", rt.updateCount)
	}
}

func TestAdminServiceReturnsReloadErrorAfterRetryExhausted(t *testing.T) {
	repo := &adminServiceRepoStub{
		providers: []config.ProviderConfig{{
			Name:    "openai",
			BaseURL: "https://api.openai.test/v1",
			APIKey:  "secret",
			Enabled: true,
		}},
		settings:        config.RoutingConfig{Selection: "priority", FailoverEnabled: true, FailoverMaxAttempts: 2},
		listModelsError: []error{errors.New("read failure"), errors.New("read failure again")},
	}
	svc := &AdminService{
		repo:           repo,
		router:         &adminServiceRouterStub{},
		auth:           auth.New(nil),
		reloadAttempts: 2,
	}

	err := svc.CreateModel(context.Background(), config.ModelConfig{
		PublicName:   "gpt-4o",
		Provider:     "openai",
		UpstreamName: "gpt-4o",
		Enabled:      true,
	})
	var adminErr *AdminError
	if !errors.As(err, &adminErr) {
		t.Fatalf("CreateModel() error = %T, want *AdminError", err)
	}
	if adminErr.Code != "api_model_reload_error" {
		t.Fatalf("error code = %q, want api_model_reload_error", adminErr.Code)
	}
}

func TestAdminServiceUpdateProviderBlankAPIKeyKeepsExistingInlineKey(t *testing.T) {
	repo := &adminServiceRepoStub{
		providers: []config.ProviderConfig{{
			Name:             "openai",
			BaseURL:          "https://api.openai.test/v1",
			APIKey:           "existing-secret",
			APIKeyRef:        "OLD_SECRET_REF",
			TimeoutSeconds:   60,
			Enabled:          true,
			AnthropicVersion: "2023-06-01",
		}},
		settings: config.RoutingConfig{Selection: "priority", FailoverEnabled: true, FailoverMaxAttempts: 2},
	}
	svc := &AdminService{
		repo:           repo,
		router:         &adminServiceRouterStub{},
		auth:           auth.New(nil),
		reloadAttempts: 1,
	}

	err := svc.UpdateProvider(context.Background(), "openai", ProviderUpdate{
		BaseURL:          "https://api.openai.test/v1",
		AnthropicVersion: "2023-06-01",
		APIKey:           "",
		APIKeySet:        true,
		APIKeyRef:        "NEW_SECRET_REF",
		APIKeyRefSet:     true,
	})
	if err != nil {
		t.Fatalf("UpdateProvider() error = %v", err)
	}
	got := repo.providers[len(repo.providers)-1]
	if got.APIKey != "existing-secret" {
		t.Fatalf("APIKey = %q, want existing-secret", got.APIKey)
	}
	if got.APIKeyRef != "NEW_SECRET_REF" {
		t.Fatalf("APIKeyRef = %q, want NEW_SECRET_REF", got.APIKeyRef)
	}
}
