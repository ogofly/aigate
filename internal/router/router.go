package router

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"aigate/internal/config"
)

const stickyTTL = time.Hour

var (
	ErrModelNotFound   = errors.New("model not found")
	ErrModelNotAllowed = errors.New("model not allowed")
)

type RouteTarget struct {
	ID            string
	ProviderName  string
	UpstreamModel string
	PublicModel   string
	Priority      int
	Weight        int
}

type Access struct {
	ModelAccess   string
	ModelRouteIDs []string
	Provider      string
}

type stickyBinding struct {
	RouteID   string
	ExpiresAt time.Time
}

type Router struct {
	mu              sync.RWMutex
	routesByModel   map[string][]config.ModelConfig
	providerEnabled map[string]bool
	models          []string
	settings        config.RoutingConfig
	sticky          map[string]stickyBinding
}

func New(models []config.ModelConfig) (*Router, error) {
	rt := &Router{}
	if err := rt.UpdateModels(models); err != nil {
		return nil, err
	}
	return rt, nil
}

func (r *Router) UpdateModels(models []config.ModelConfig) error {
	providers := make([]config.ProviderConfig, 0)
	providerSet := make(map[string]struct{})
	normalized := make([]config.ModelConfig, 0, len(models))
	for _, model := range models {
		model.Enabled = true
		if model.Weight <= 0 {
			model.Weight = 1
		}
		normalized = append(normalized, model)
		if model.Provider == "" {
			continue
		}
		if _, ok := providerSet[model.Provider]; ok {
			continue
		}
		providerSet[model.Provider] = struct{}{}
		providers = append(providers, config.ProviderConfig{Name: model.Provider, Enabled: true})
	}
	settings := config.RoutingConfig{}
	settings.SetDefaults()
	return r.Update(normalized, providers, settings)
}

func (r *Router) Update(models []config.ModelConfig, providers []config.ProviderConfig, settings config.RoutingConfig) error {
	routesByModel := make(map[string][]config.ModelConfig)
	publicSet := make(map[string]struct{})
	for _, model := range models {
		model.SetDefaults()
		if model.ID == "" {
			model.ID = routeIDFromParts(model.PublicName, model.Provider, model.UpstreamName)
		}
		routesByModel[model.PublicName] = append(routesByModel[model.PublicName], model)
		publicSet[model.PublicName] = struct{}{}
	}
	publicModels := make([]string, 0, len(publicSet))
	for model := range publicSet {
		publicModels = append(publicModels, model)
	}
	sort.Strings(publicModels)
	for model := range routesByModel {
		sortRoutes(routesByModel[model])
	}

	providerEnabled := make(map[string]bool, len(providers))
	for _, provider := range providers {
		providerEnabled[provider.Name] = provider.Enabled
	}
	settings.SetDefaults()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.routesByModel = routesByModel
	r.providerEnabled = providerEnabled
	r.models = publicModels
	r.settings = settings
	if r.sticky == nil {
		r.sticky = make(map[string]stickyBinding)
	}
	return nil
}

func (r *Router) Resolve(model string) (RouteTarget, error) {
	plan, err := r.ResolvePlan(model, Access{ModelAccess: "all"}, "")
	if err != nil {
		return RouteTarget{}, err
	}
	return plan[0], nil
}

func (r *Router) ResolvePlan(model string, access Access, sessionSeed string) ([]RouteTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	allRoutes, exists := r.routesByModel[model]
	if !exists || len(allRoutes) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrModelNotFound, model)
	}
	candidates := r.filterCandidatesLocked(allRoutes, access)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrModelNotAllowed, model)
	}

	first := r.selectFirstLocked(model, candidates, access, sessionSeed)
	targets := make([]RouteTarget, 0, len(candidates))
	targets = append(targets, toTarget(first))
	for _, candidate := range candidates {
		if candidate.ID == first.ID {
			continue
		}
		targets = append(targets, toTarget(candidate))
	}
	return targets, nil
}

func (r *Router) ListModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.models))
	copy(out, r.models)
	return out
}

func (r *Router) ListModelsForAccess(access Access) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.models))
	for _, model := range r.models {
		if len(r.filterCandidatesLocked(r.routesByModel[model], access)) > 0 {
			out = append(out, model)
		}
	}
	return out
}

func (r *Router) ListProvidersForAccess(model string, access Access) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	candidates := r.filterCandidatesLocked(r.routesByModel[model], access)
	providerSet := make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate.Provider != "" {
			providerSet[candidate.Provider] = struct{}{}
		}
	}
	out := make([]string, 0, len(providerSet))
	for provider := range providerSet {
		out = append(out, provider)
	}
	sort.Strings(out)
	return out
}

func (r *Router) filterCandidatesLocked(routes []config.ModelConfig, access Access) []config.ModelConfig {
	allowedRoutes := make(map[string]struct{})
	if strings.EqualFold(strings.TrimSpace(access.ModelAccess), "selected") {
		for _, id := range access.ModelRouteIDs {
			id = strings.TrimSpace(id)
			if id != "" {
				allowedRoutes[id] = struct{}{}
			}
		}
	}

	candidates := make([]config.ModelConfig, 0, len(routes))
	providerOverride := strings.TrimSpace(access.Provider)
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		if providerOverride != "" && route.Provider != providerOverride {
			continue
		}
		if enabled, ok := r.providerEnabled[route.Provider]; ok && !enabled {
			continue
		}
		if len(allowedRoutes) > 0 {
			if _, ok := allowedRoutes[route.ID]; !ok {
				continue
			}
		}
		if strings.EqualFold(strings.TrimSpace(access.ModelAccess), "selected") && len(allowedRoutes) == 0 {
			continue
		}
		candidates = append(candidates, route)
	}
	sortRoutes(candidates)
	return candidates
}

func (r *Router) selectFirstLocked(model string, candidates []config.ModelConfig, access Access, sessionSeed string) config.ModelConfig {
	selection := strings.TrimSpace(r.settings.Selection)
	if selection == "" {
		selection = "priority"
	}
	switch selection {
	case "weight", "random":
		if sessionHash := stickyHash(model, access, sessionSeed); sessionHash != "" {
			if binding, ok := r.sticky[sessionHash]; ok {
				if time.Now().Before(binding.ExpiresAt) {
					for _, candidate := range candidates {
						if candidate.ID == binding.RouteID {
							return candidate
						}
					}
				}
				delete(r.sticky, sessionHash)
			}
			selected := selectByStrategy(selection, candidates)
			r.sticky[sessionHash] = stickyBinding{RouteID: selected.ID, ExpiresAt: time.Now().Add(stickyTTL)}
			return selected
		}
		return selectByStrategy(selection, candidates)
	default:
		return candidates[0]
	}
}

func selectByStrategy(selection string, candidates []config.ModelConfig) config.ModelConfig {
	if len(candidates) == 1 {
		return candidates[0]
	}
	switch selection {
	case "random":
		return candidates[rand.Intn(len(candidates))]
	case "weight":
		total := 0
		for _, candidate := range candidates {
			if candidate.Weight > 0 {
				total += candidate.Weight
			}
		}
		if total <= 0 {
			return candidates[rand.Intn(len(candidates))]
		}
		pick := rand.Intn(total)
		for _, candidate := range candidates {
			if candidate.Weight <= 0 {
				continue
			}
			if pick < candidate.Weight {
				return candidate
			}
			pick -= candidate.Weight
		}
	}
	return candidates[0]
}

func sortRoutes(routes []config.ModelConfig) {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Priority == routes[j].Priority {
			return routes[i].ID < routes[j].ID
		}
		return routes[i].Priority < routes[j].Priority
	})
}

func toTarget(route config.ModelConfig) RouteTarget {
	weight := route.Weight
	if weight <= 0 {
		weight = 1
	}
	return RouteTarget{
		ID:            route.ID,
		ProviderName:  route.Provider,
		UpstreamModel: route.UpstreamName,
		PublicModel:   route.PublicName,
		Priority:      route.Priority,
		Weight:        weight,
	}
}

func stickyHash(model string, access Access, seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		model,
		strings.TrimSpace(access.ModelAccess),
		strings.Join(access.ModelRouteIDs, ","),
		strings.TrimSpace(access.Provider),
		seed,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func routeIDFromParts(publicName, provider, upstreamName string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{publicName, provider, upstreamName}, "\x00")))
	return "mrt_" + hex.EncodeToString(sum[:16])
}
