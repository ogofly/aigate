package router_test

import (
	"testing"

	"aigate/internal/config"
	"aigate/internal/router"
)

func TestResolve(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	target, err := rt.Resolve("gpt-4o-mini")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if target.ProviderName != "openai" {
		t.Fatalf("ProviderName = %q, want %q", target.ProviderName, "openai")
	}

	if target.UpstreamModel != "gpt-4o-mini" {
		t.Fatalf("UpstreamModel = %q, want %q", target.UpstreamModel, "gpt-4o-mini")
	}
}

func TestResolveMissingModel(t *testing.T) {
	rt, err := router.New([]config.ModelConfig{
		{
			PublicName:   "gpt-4o-mini",
			Provider:     "openai",
			UpstreamName: "gpt-4o-mini",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := rt.Resolve("missing-model"); err == nil {
		t.Fatal("Resolve() error = nil, want non-nil")
	}
}

func TestResolvePlanUsesPriorityForDuplicatePublicName(t *testing.T) {
	rt, err := router.New(nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = rt.Update([]config.ModelConfig{
		{ID: "mrt_b", PublicName: "gpt-4o", Provider: "openai-b", UpstreamName: "gpt-4o-b", Priority: 10, Enabled: true},
		{ID: "mrt_a", PublicName: "gpt-4o", Provider: "openai-a", UpstreamName: "gpt-4o-a", Priority: 0, Enabled: true},
	}, []config.ProviderConfig{
		{Name: "openai-a", Enabled: true},
		{Name: "openai-b", Enabled: true},
	}, config.RoutingConfig{Selection: "priority"})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	plan, err := rt.ResolvePlan("gpt-4o", router.Access{ModelAccess: "all"}, "")
	if err != nil {
		t.Fatalf("ResolvePlan() error = %v", err)
	}
	if plan[0].ID != "mrt_a" {
		t.Fatalf("first route = %q, want mrt_a", plan[0].ID)
	}
}

func TestListModelsForAccessFiltersDisabledProviderRouteAndSelectedKey(t *testing.T) {
	rt, err := router.New(nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = rt.Update([]config.ModelConfig{
		{ID: "mrt_allowed", PublicName: "gpt-4o", Provider: "openai-a", UpstreamName: "gpt-4o", Enabled: true},
		{ID: "mrt_disabled_route", PublicName: "gpt-4o", Provider: "openai-a", UpstreamName: "gpt-4o-disabled", Enabled: false},
		{ID: "mrt_disabled_provider", PublicName: "hidden", Provider: "openai-b", UpstreamName: "hidden", Enabled: true},
	}, []config.ProviderConfig{
		{Name: "openai-a", Enabled: true},
		{Name: "openai-b", Enabled: false},
	}, config.RoutingConfig{Selection: "priority"})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	models := rt.ListModelsForAccess(router.Access{ModelAccess: "selected", ModelRouteIDs: []string{"mrt_allowed"}})
	if len(models) != 1 || models[0] != "gpt-4o" {
		t.Fatalf("models = %#v, want only gpt-4o", models)
	}
	if _, err := rt.ResolvePlan("hidden", router.Access{ModelAccess: "all"}, ""); err == nil {
		t.Fatal("ResolvePlan(hidden) error = nil, want disabled provider filtered")
	}
	if _, err := rt.ResolvePlan("gpt-4o", router.Access{ModelAccess: "selected"}, ""); err == nil {
		t.Fatal("ResolvePlan(selected without routes) error = nil, want not allowed")
	}
}

func TestResolvePlanFiltersProviderOverride(t *testing.T) {
	rt, err := router.New(nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = rt.Update([]config.ModelConfig{
		{ID: "mrt_a", PublicName: "gpt-4o", Provider: "openai-a", UpstreamName: "gpt-4o-a", Priority: 10, Enabled: true},
		{ID: "mrt_b", PublicName: "gpt-4o", Provider: "openai-b", UpstreamName: "gpt-4o-b", Priority: 0, Enabled: true},
	}, []config.ProviderConfig{
		{Name: "openai-a", Enabled: true},
		{Name: "openai-b", Enabled: true},
	}, config.RoutingConfig{Selection: "priority"})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	plan, err := rt.ResolvePlan("gpt-4o", router.Access{ModelAccess: "all", Provider: "openai-a"}, "")
	if err != nil {
		t.Fatalf("ResolvePlan() error = %v", err)
	}
	if len(plan) != 1 || plan[0].ProviderName != "openai-a" || plan[0].UpstreamModel != "gpt-4o-a" {
		t.Fatalf("plan = %#v, want only openai-a route", plan)
	}

	providers := rt.ListProvidersForAccess("gpt-4o", router.Access{ModelAccess: "all"})
	if len(providers) != 2 || providers[0] != "openai-a" || providers[1] != "openai-b" {
		t.Fatalf("providers = %#v, want [openai-a openai-b]", providers)
	}
	if _, err := rt.ResolvePlan("gpt-4o", router.Access{ModelAccess: "selected", ModelRouteIDs: []string{"mrt_b"}, Provider: "openai-a"}, ""); err == nil {
		t.Fatal("ResolvePlan(selected provider without allowed route) error = nil, want not allowed")
	}
}

func TestResolvePlanStickySessionRebindsWhenRouteUnavailable(t *testing.T) {
	rt, err := router.New(nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	routes := []config.ModelConfig{
		{ID: "mrt_a", PublicName: "gpt-4o", Provider: "openai", UpstreamName: "a", Weight: 1, Enabled: true},
		{ID: "mrt_b", PublicName: "gpt-4o", Provider: "openai", UpstreamName: "b", Weight: 1, Enabled: true},
	}
	providers := []config.ProviderConfig{{Name: "openai", Enabled: true}}
	settings := config.RoutingConfig{Selection: "random"}
	if err := rt.Update(routes, providers, settings); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	first, err := rt.ResolvePlan("gpt-4o", router.Access{ModelAccess: "all"}, "session-1")
	if err != nil {
		t.Fatalf("ResolvePlan() error = %v", err)
	}
	second, err := rt.ResolvePlan("gpt-4o", router.Access{ModelAccess: "all"}, "session-1")
	if err != nil {
		t.Fatalf("ResolvePlan() second error = %v", err)
	}
	if first[0].ID != second[0].ID {
		t.Fatalf("sticky route changed: %q then %q", first[0].ID, second[0].ID)
	}
	for i := range routes {
		if routes[i].ID == first[0].ID {
			routes[i].Enabled = false
		}
	}
	if err := rt.Update(routes, providers, settings); err != nil {
		t.Fatalf("Update disabled route error = %v", err)
	}
	third, err := rt.ResolvePlan("gpt-4o", router.Access{ModelAccess: "all"}, "session-1")
	if err != nil {
		t.Fatalf("ResolvePlan() third error = %v", err)
	}
	if third[0].ID == first[0].ID {
		t.Fatalf("sticky route = %q, want rebind after disable", third[0].ID)
	}
}
