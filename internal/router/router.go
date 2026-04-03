package router

import (
	"fmt"
	"sort"

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
	routes map[string]RouteTarget
	models []string
}

func New(providers map[string]provider.Provider, models []config.ModelConfig) (*Router, error) {
	routes := make(map[string]RouteTarget, len(models))
	publicModels := make([]string, 0, len(models))

	for _, model := range models {
		p, ok := providers[model.Provider]
		if !ok {
			return nil, fmt.Errorf("unknown provider %q for model %q", model.Provider, model.PublicName)
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

	return &Router{
		routes: routes,
		models: publicModels,
	}, nil
}

func (r *Router) Resolve(model string) (RouteTarget, error) {
	target, ok := r.routes[model]
	if !ok {
		return RouteTarget{}, fmt.Errorf("model %q not found", model)
	}
	return target, nil
}

func (r *Router) ListModels() []string {
	out := make([]string, len(r.models))
	copy(out, r.models)
	return out
}
