package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"aigate/internal/store"
	"aigate/internal/usage"
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
