package router

import (
	"fmt"
	"sort"
	"sync"

	"aigate/internal/config"
	"aigate/internal/provider"
)

type RouteTarget struct {
	Provider      provider.Provider
	ProviderName  string
	UpstreamModel string
	PublicModel   string
}

type Router struct {
	mu        sync.RWMutex
	providers map[string]provider.Provider
	routes    map[string]RouteTarget
	models    []string
}

func New(providers map[string]provider.Provider, models []config.ModelConfig) (*Router, error) {
	rt := &Router{
		providers: providers,
	}
	if err := rt.UpdateModels(models); err != nil {
		return nil, err
	}
	return rt, nil
}

func (r *Router) UpdateModels(models []config.ModelConfig) error {
	r.mu.RLock()
	providers := r.providers
	r.mu.RUnlock()
	return r.update(models, providers)
}

func (r *Router) UpdateProvidersAndModels(providers map[string]provider.Provider, models []config.ModelConfig) error {
	return r.update(models, providers)
}

func (r *Router) update(models []config.ModelConfig, providers map[string]provider.Provider) error {
	routes := make(map[string]RouteTarget, len(models))
	publicModels := make([]string, 0, len(models))

	for _, model := range models {
		p, ok := providers[model.Provider]
		if !ok {
			return fmt.Errorf("unknown provider %q for model %q", model.Provider, model.PublicName)
		}
		routes[model.PublicName] = RouteTarget{
			Provider:      p,
			ProviderName:  model.Provider,
			UpstreamModel: model.UpstreamName,
			PublicModel:   model.PublicName,
		}
		publicModels = append(publicModels, model.PublicName)
	}
	sort.Strings(publicModels)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = providers
	r.routes = routes
	r.models = publicModels
	return nil
}

func (r *Router) Resolve(model string) (RouteTarget, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	target, ok := r.routes[model]
	if !ok {
		return RouteTarget{}, fmt.Errorf("model %q not found", model)
	}
	return target, nil
}

func (r *Router) ListModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.models))
	copy(out, r.models)
	return out
}
