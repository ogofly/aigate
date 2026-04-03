package store_test

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"aigate/internal/store"
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
	if providers[0].APIKeyRef != "OPENAI_API_KEY" {
		t.Fatalf("APIKeyRef = %q, want %q", providers[0].APIKeyRef, "OPENAI_API_KEY")
	}
}
