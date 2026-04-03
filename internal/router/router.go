package router

import (
	"fmt"
	"sort"
	"sync"

	"aigate/internal/config"
)

type RouteTarget struct {
	ProviderName  string
	UpstreamModel string
	PublicModel   string
}

type Router struct {
	mu     sync.RWMutex
	routes map[string]RouteTarget
	models []string
}

func New(models []config.ModelConfig) (*Router, error) {
	rt := &Router{}
	if err := rt.UpdateModels(models); err != nil {
		return nil, err
	}
	return rt, nil
}

func (r *Router) UpdateModels(models []config.ModelConfig) error {
	routes := make(map[string]RouteTarget, len(models))
	publicModels := make([]string, 0, len(models))

	for _, model := range models {
		routes[model.PublicName] = RouteTarget{
			ProviderName:  model.Provider,
			UpstreamModel: model.UpstreamName,
			PublicModel:   model.PublicName,
		}
		publicModels = append(publicModels, model.PublicName)
	}
	sort.Strings(publicModels)

	r.mu.Lock()
	defer r.mu.Unlock()
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
