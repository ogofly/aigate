package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"llmgate/internal/config"
	"llmgate/internal/store"
	"llmgate/internal/usage"
)

func TestNewSQLiteMigratesLegacyProvidersTable(t *testing.T) {
	path := t.TempDir() + "/legacy.db"

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE providers (
			name TEXT PRIMARY KEY,
			base_url TEXT NOT NULL,
			api_key TEXT NOT NULL,
			timeout_seconds INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		);
		INSERT INTO providers(name, base_url, api_key, timeout_seconds, updated_at)
		VALUES ('openai', 'https://api.openai.com/v1', 'OPENAI_API_KEY', 60, '2026-01-01T00:00:00Z');
	`); err != nil {
		t.Fatalf("seed legacy db error = %v", err)
	}
	_ = db.Close()

	sqliteStore, err := store.NewSQLite(path)
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	defer sqliteStore.Close()

	providers, err := sqliteStore.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders() error = %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("len(providers) = %d, want %d", len(providers), 1)
	}
	if providers[0].APIKey != "OPENAI_API_KEY" {
		t.Fatalf("APIKey = %q, want %q", providers[0].APIKey, "OPENAI_API_KEY")
	}
	if providers[0].APIKeyRef != "" {
		t.Fatalf("APIKeyRef = %q, want empty", providers[0].APIKeyRef)
	}
}

func TestNewSQLiteMigratesLegacyProvidersTableWithAPIKeyRef(t *testing.T) {
	path := t.TempDir() + "/legacy-ref.db"

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE providers (
			name TEXT PRIMARY KEY,
			base_url TEXT NOT NULL,
			api_key_ref TEXT NOT NULL,
			timeout_seconds INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		);
		INSERT INTO providers(name, base_url, api_key_ref, timeout_seconds, updated_at)
		VALUES ('openai', 'https://api.openai.com/v1', 'OPENAI_API_KEY', 60, '2026-01-01T00:00:00Z');
	`); err != nil {
		t.Fatalf("seed legacy db error = %v", err)
	}
	_ = db.Close()

	sqliteStore, err := store.NewSQLite(path)
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	defer sqliteStore.Close()

	providers, err := sqliteStore.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders() error = %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("len(providers) = %d, want %d", len(providers), 1)
	}
	if providers[0].APIKey != "" {
		t.Fatalf("APIKey = %q, want empty", providers[0].APIKey)
	}
	if providers[0].APIKeyRef != "OPENAI_API_KEY" {
		t.Fatalf("APIKeyRef = %q, want %q", providers[0].APIKeyRef, "OPENAI_API_KEY")
	}
}

func TestNewSQLiteMigratesLegacyUsageRollupsTable(t *testing.T) {
	path := t.TempDir() + "/legacy-usage.db"

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE usage_rollups (
			bucket_start TEXT NOT NULL,
			api_key TEXT NOT NULL,
			key_name TEXT NOT NULL DEFAULT '',
			owner TEXT NOT NULL DEFAULT '',
			purpose TEXT NOT NULL DEFAULT '',
			endpoint TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT '',
			public_model TEXT NOT NULL DEFAULT '',
			upstream_model TEXT NOT NULL DEFAULT '',
			request_count INTEGER NOT NULL DEFAULT 0,
			success_count INTEGER NOT NULL DEFAULT 0,
			error_count INTEGER NOT NULL DEFAULT 0,
			request_tokens INTEGER NOT NULL DEFAULT 0,
			response_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (bucket_start, api_key, endpoint, provider, public_model, upstream_model)
		);
		INSERT INTO usage_rollups(
			bucket_start, api_key, key_name, owner, purpose, endpoint, provider, public_model, upstream_model,
			request_count, success_count, error_count, request_tokens, response_tokens, total_tokens
		) VALUES (
			'2026-01-01T00:00:00Z', 'sk-app-001', 'alice', 'alice', 'debug', 'chat.completions', 'openai', 'gpt-4o-mini', 'gpt-4o-mini',
			1, 1, 0, 10, 5, 15
		);
	`); err != nil {
		t.Fatalf("seed legacy usage db error = %v", err)
	}
	_ = db.Close()

	sqliteStore, err := store.NewSQLite(path)
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	defer sqliteStore.Close()

	summaries, err := sqliteStore.UsageSummaries(context.Background())
	if err != nil {
		t.Fatalf("UsageSummaries() error = %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want %d", len(summaries), 1)
	}
	if summaries[0].KeyID != usage.KeyID("sk-app-001") {
		t.Fatalf("KeyID = %q, want %q", summaries[0].KeyID, usage.KeyID("sk-app-001"))
	}
	if summaries[0].APIKey != "" {
		t.Fatalf("APIKey = %q, want empty", summaries[0].APIKey)
	}
}

func TestNewSQLiteMigratesLegacyModelsToRoutes(t *testing.T) {
	path := t.TempDir() + "/legacy-models.db"

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE models (
			public_name TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			upstream_name TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		INSERT INTO models(public_name, provider, upstream_name, updated_at)
		VALUES ('gpt-4o', 'openai', 'gpt-4o', '2026-01-01T00:00:00Z');
	`); err != nil {
		t.Fatalf("seed legacy models db error = %v", err)
	}
	_ = db.Close()

	sqliteStore, err := store.NewSQLite(path)
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	defer sqliteStore.Close()

	models, err := sqliteStore.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].ID == "" || models[0].ID[:4] != "mrt_" {
		t.Fatalf("route id = %q, want mrt_ prefix", models[0].ID)
	}
	if models[0].Priority != 0 || models[0].Weight != 1 || !models[0].Enabled {
		t.Fatalf("migrated route defaults = priority %d weight %d enabled %v", models[0].Priority, models[0].Weight, models[0].Enabled)
	}
}

func TestSQLiteModelsAllowDuplicatePublicNameButKeepUniqueRouteTriplet(t *testing.T) {
	sqliteStore, err := store.NewSQLite(t.TempDir() + "/routes.db")
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	defer sqliteStore.Close()

	ctx := context.Background()
	routes := []config.ModelConfig{
		{PublicName: "gpt-4o", Provider: "openai-a", UpstreamName: "gpt-4o", Enabled: true},
		{PublicName: "gpt-4o", Provider: "openai-b", UpstreamName: "gpt-4o", Enabled: true},
	}
	for _, route := range routes {
		if err := sqliteStore.UpsertModel(ctx, route); err != nil {
			t.Fatalf("UpsertModel() error = %v", err)
		}
	}
	if err := sqliteStore.UpsertModel(ctx, config.ModelConfig{PublicName: "gpt-4o", Provider: "openai-a", UpstreamName: "gpt-4o", Priority: 5, Enabled: true}); err != nil {
		t.Fatalf("duplicate route UpsertModel() error = %v", err)
	}
	models, err := sqliteStore.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2 unique route triplets", len(models))
	}
}

func TestQueryUsageTrendGroupsByLocalTime(t *testing.T) {
	// Force a specific timezone: UTC+8
	time.Local = time.FixedZone("UTC+8", 8*3600)
	defer func() { time.Local = time.UTC }()

	path := t.TempDir() + "/trend.db"
	s, err := store.NewSQLite(path)
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	defer s.Close()

	// Insert UTC buckets: 2026-04-28 10:00 UTC = 2026-04-28 18:00 +08:00
	// and 2026-04-28 16:00 UTC = 2026-04-29 00:00 +08:00 (next day in local)
	// and 2026-04-28 17:00 UTC = 2026-04-29 01:00 +08:00
	// So in local time, data spans Apr 28 (18:00 bucket) and Apr 29 (00:00 and 01:00 buckets)
	buckets := []usage.Rollup{
		{
			BucketStart:    time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC),
			KeyID:          "k1",
			KeyName:        "test",
			RequestCount:   1,
			SuccessCount:   1,
			ErrorCount:     0,
			RequestTokens:  10,
			ResponseTokens: 20,
			TotalTokens:    30,
		},
		{
			BucketStart:    time.Date(2026, 4, 28, 16, 0, 0, 0, time.UTC),
			KeyID:          "k1",
			KeyName:        "test",
			RequestCount:   2,
			SuccessCount:   2,
			ErrorCount:     0,
			RequestTokens:  40,
			ResponseTokens: 80,
			TotalTokens:    120,
		},
		{
			BucketStart:    time.Date(2026, 4, 28, 17, 0, 0, 0, time.UTC),
			KeyID:          "k1",
			KeyName:        "test",
			RequestCount:   3,
			SuccessCount:   1,
			ErrorCount:     2,
			RequestTokens:  60,
			ResponseTokens: 100,
			TotalTokens:    160,
		},
	}
	if err := s.UpsertUsageRollups(context.Background(), buckets); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	// Query by day (groupBy != "hour"): should produce 2 groups (Apr 28 and Apr 29)
	// Need start=2026-04-28, end=2026-04-29 in +08:00 to cover all 3 UTC buckets
	// 2026-04-28 +08:00 = 2026-04-27 16:00 UTC; 2026-04-30 +08:00 = 2026-04-29 16:00 UTC
	start := time.Date(2026, 4, 28, 0, 0, 0, 0, time.Local)
	end := time.Date(2026, 4, 29, 0, 0, 0, 0, time.Local)
	filter := store.UsageFilter{StartTime: start, EndTime: end}

	points, err := s.QueryUsageTrend(context.Background(), filter, "day")
	if err != nil {
		t.Fatalf("QueryUsageTrend() error = %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("len(points) = %d, want 2 (Apr 28 and Apr 29 in +08:00)", len(points))
	}
	if points[0].Date != "2026-04-28" {
		t.Errorf("points[0].Date = %q, want %q", points[0].Date, "2026-04-28")
	}
	if points[0].TotalTokens != 30 {
		t.Errorf("points[0].TotalTokens = %d, want 30", points[0].TotalTokens)
	}
	if points[1].Date != "2026-04-29" {
		t.Errorf("points[1].Date = %q, want %q", points[1].Date, "2026-04-29")
	}
	if points[1].TotalTokens != 280 {
		t.Errorf("points[1].TotalTokens = %d, want 280", points[1].TotalTokens)
	}

	// Query by hour: should produce 3 groups
	pointsH, err := s.QueryUsageTrend(context.Background(), filter, "hour")
	if err != nil {
		t.Fatalf("QueryUsageTrend(hour) error = %v", err)
	}
	if len(pointsH) != 3 {
		t.Fatalf("len(pointsH) = %d, want 3 (18:00, 00:00, 01:00 in +08:00)", len(pointsH))
	}
	if pointsH[0].Date != "2026-04-28 18:00" {
		t.Errorf("pointsH[0].Date = %q, want %q", pointsH[0].Date, "2026-04-28 18:00")
	}
	if pointsH[1].Date != "2026-04-29 00:00" {
		t.Errorf("pointsH[1].Date = %q, want %q", pointsH[1].Date, "2026-04-29 00:00")
	}
	if pointsH[2].Date != "2026-04-29 01:00" {
		t.Errorf("pointsH[2].Date = %q, want %q", pointsH[2].Date, "2026-04-29 01:00")
	}
}

func TestQueryUsageRollupsFilters(t *testing.T) {
	path := t.TempDir() + "/rollups.db"
	s, err := store.NewSQLite(path)
	if err != nil {
		t.Fatalf("store.NewSQLite() error = %v", err)
	}
	defer s.Close()

	rollups := []usage.Rollup{
		{
			BucketStart:    time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-alice-001"),
			KeyName:        "alice-key",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   1,
			SuccessCount:   1,
			RequestTokens:  10,
			ResponseTokens: 5,
			TotalTokens:    15,
		},
		{
			BucketStart:    time.Date(2026, 5, 10, 1, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-alice-001"),
			KeyName:        "alice-key",
			Owner:          "alice",
			Purpose:        "debug",
			Endpoint:       "chat.completions",
			Provider:       "deepseek",
			PublicModel:    "deepseek-chat",
			UpstreamModel:  "deepseek-chat",
			RequestCount:   2,
			SuccessCount:   2,
			RequestTokens:  20,
			ResponseTokens: 10,
			TotalTokens:    30,
		},
		{
			BucketStart:    time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
			KeyID:          usage.KeyID("sk-bob-001"),
			KeyName:        "bob-key",
			Owner:          "bob",
			Purpose:        "prod",
			Endpoint:       "chat.completions",
			Provider:       "openai",
			PublicModel:    "gpt-4o-mini",
			UpstreamModel:  "gpt-4o-mini",
			RequestCount:   3,
			SuccessCount:   2,
			ErrorCount:     1,
			RequestTokens:  30,
			ResponseTokens: 15,
			TotalTokens:    45,
		},
	}
	if err := s.UpsertUsageRollups(context.Background(), rollups); err != nil {
		t.Fatalf("UpsertUsageRollups() error = %v", err)
	}

	filter := store.UsageFilter{
		StartTime: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		KeyID:     usage.KeyID("sk-alice-001"),
		Model:     "gpt-4o-mini",
		Provider:  "openai",
		Owner:     "alice",
	}
	got, err := s.QueryUsageRollups(context.Background(), filter)
	if err != nil {
		t.Fatalf("QueryUsageRollups() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(rollups) = %d, want %d", len(got), 1)
	}
	if got[0].KeyName != "alice-key" || got[0].Owner != "alice" || got[0].PublicModel != "gpt-4o-mini" || got[0].TotalTokens != 15 {
		t.Fatalf("unexpected rollup: %+v", got[0])
	}

	got, err = s.QueryUsageRollups(context.Background(), store.UsageFilter{
		StartTime: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		Owner:     "alice",
	})
	if err != nil {
		t.Fatalf("QueryUsageRollups(owner) error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(owner rollups) = %d, want %d", len(got), 2)
	}

	got, err = s.QueryUsageRollups(context.Background(), store.UsageFilter{
		StartTime: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		Provider:  "deepseek",
	})
	if err != nil {
		t.Fatalf("QueryUsageRollups(provider) error = %v", err)
	}
	if len(got) != 1 || got[0].Provider != "deepseek" || got[0].PublicModel != "deepseek-chat" {
		t.Fatalf("unexpected provider filtered rollups: %+v", got)
	}

	summaries, err := s.QueryUsage(context.Background(), store.UsageFilter{Provider: "openai"})
	if err != nil {
		t.Fatalf("QueryUsage(provider) error = %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("len(provider summaries) = %d, want %d", len(summaries), 2)
	}

	points, err := s.QueryUsageTrend(context.Background(), store.UsageFilter{
		StartTime: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		Provider:  "deepseek",
	}, "day")
	if err != nil {
		t.Fatalf("QueryUsageTrend(provider) error = %v", err)
	}
	if len(points) != 1 || points[0].RequestCount != 2 {
		t.Fatalf("unexpected provider trend points: %+v", points)
	}

	models, err := s.QueryUsageByModel(context.Background(), store.UsageFilter{Provider: "deepseek"})
	if err != nil {
		t.Fatalf("QueryUsageByModel(provider) error = %v", err)
	}
	if len(models) != 1 || models[0].Model != "deepseek-chat" || models[0].RequestCount != 2 {
		t.Fatalf("unexpected provider model summaries: %+v", models)
	}

	providers, err := s.ListUsageProviders(context.Background())
	if err != nil {
		t.Fatalf("ListUsageProviders() error = %v", err)
	}
	if len(providers) != 2 || providers[0] != "deepseek" || providers[1] != "openai" {
		t.Fatalf("providers = %+v, want [deepseek openai]", providers)
	}

	got, err = s.QueryUsageRollups(context.Background(), store.UsageFilter{
		StartTime: time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		Owner:     "bob",
	})
	if err != nil {
		t.Fatalf("QueryUsageRollups(date) error = %v", err)
	}
	if len(got) != 1 || got[0].KeyName != "bob-key" {
		t.Fatalf("unexpected date filtered rollups: %+v", got)
	}
}
