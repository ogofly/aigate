package store_test

import (
	"context"
	"database/sql"
	"testing"

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
