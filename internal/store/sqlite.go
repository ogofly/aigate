package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"aigate/internal/config"
	"aigate/internal/usage"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLite(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	store := &SQLiteStore{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) init(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS providers (
			name TEXT PRIMARY KEY,
			base_url TEXT NOT NULL,
			api_key_ref TEXT NOT NULL,
			timeout_seconds INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS models (
			public_name TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			upstream_name TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS auth_keys (
			key TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			owner TEXT NOT NULL DEFAULT '',
			purpose TEXT NOT NULL DEFAULT '',
			admin INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS usage_rollups (
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
		)`,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.migrateProvidersTable(ctx); err != nil {
		return err
	}
	return s.migrateAuthKeysTable(ctx)
}

func (s *SQLiteStore) migrateProvidersTable(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(providers)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var hasAPIKey bool
	var hasAPIKeyRef bool
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return err
		}
		switch strings.ToLower(name) {
		case "api_key":
			hasAPIKey = true
		case "api_key_ref":
			hasAPIKeyRef = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hasAPIKeyRef || !hasAPIKey {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`ALTER TABLE providers RENAME TO providers_old`,
		`CREATE TABLE providers (
			name TEXT PRIMARY KEY,
			base_url TEXT NOT NULL,
			api_key_ref TEXT NOT NULL,
			timeout_seconds INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`INSERT INTO providers(name, base_url, api_key_ref, timeout_seconds, updated_at)
		 SELECT name, base_url, api_key, timeout_seconds, updated_at FROM providers_old`,
		`DROP TABLE providers_old`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) migrateAuthKeysTable(context.Context) error {
	return nil
}

func (s *SQLiteStore) SeedProvidersIfEmpty(ctx context.Context, providers []config.ProviderConfig) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM providers`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	for _, provider := range providers {
		if err := s.UpsertProvider(ctx, provider); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) SeedModelsIfEmpty(ctx context.Context, models []config.ModelConfig) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM models`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	for _, model := range models {
		if err := s.UpsertModel(ctx, model); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) SeedAuthKeysIfEmpty(ctx context.Context, keys []config.KeyConfig) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_keys`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	for _, key := range keys {
		if err := s.UpsertAuthKey(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) ListProviders(ctx context.Context) ([]config.ProviderConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, base_url, api_key_ref, timeout_seconds FROM providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []config.ProviderConfig
	for rows.Next() {
		var provider config.ProviderConfig
		if err := rows.Scan(&provider.Name, &provider.BaseURL, &provider.APIKeyRef, &provider.TimeoutSeconds); err != nil {
			return nil, err
		}
		out = append(out, provider)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListModels(ctx context.Context) ([]config.ModelConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT public_name, provider, upstream_name FROM models ORDER BY public_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []config.ModelConfig
	for rows.Next() {
		var model config.ModelConfig
		if err := rows.Scan(&model.PublicName, &model.Provider, &model.UpstreamName); err != nil {
			return nil, err
		}
		out = append(out, model)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListAuthKeys(ctx context.Context) ([]config.KeyConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, name, owner, purpose, admin FROM auth_keys ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []config.KeyConfig
	for rows.Next() {
		var item config.KeyConfig
		var adminInt int
		if err := rows.Scan(&item.Key, &item.Name, &item.Owner, &item.Purpose, &adminInt); err != nil {
			return nil, err
		}
		item.Admin = adminInt != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpsertProvider(ctx context.Context, provider config.ProviderConfig) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO providers(name, base_url, api_key_ref, timeout_seconds, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			base_url = excluded.base_url,
			api_key_ref = excluded.api_key_ref,
			timeout_seconds = excluded.timeout_seconds,
			updated_at = excluded.updated_at
	`, provider.Name, provider.BaseURL, provider.APIKeyRef, provider.TimeoutSeconds, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) UpsertModel(ctx context.Context, model config.ModelConfig) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO models(public_name, provider, upstream_name, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(public_name) DO UPDATE SET
			provider = excluded.provider,
			upstream_name = excluded.upstream_name,
			updated_at = excluded.updated_at
	`, model.PublicName, model.Provider, model.UpstreamName, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) DeleteProvider(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE name = ?`, name)
	return err
}

func (s *SQLiteStore) DeleteModel(ctx context.Context, publicName string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM models WHERE public_name = ?`, publicName)
	return err
}

func (s *SQLiteStore) UpsertAuthKey(ctx context.Context, key config.KeyConfig) error {
	adminInt := 0
	if key.Admin {
		adminInt = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_keys(key, name, owner, purpose, admin, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			name = excluded.name,
			owner = excluded.owner,
			purpose = excluded.purpose,
			admin = excluded.admin,
			updated_at = excluded.updated_at
	`, key.Key, key.Name, key.Owner, key.Purpose, adminInt, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) DeleteAuthKey(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_keys WHERE key = ?`, key)
	return err
}

func (s *SQLiteStore) UpsertUsageRollups(ctx context.Context, rollups []usage.Rollup) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_rollups(
			bucket_start, api_key, key_name, owner, purpose, endpoint, provider, public_model, upstream_model,
			request_count, success_count, error_count, request_tokens, response_tokens, total_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bucket_start, api_key, endpoint, provider, public_model, upstream_model) DO UPDATE SET
			request_count = usage_rollups.request_count + excluded.request_count,
			success_count = usage_rollups.success_count + excluded.success_count,
			error_count = usage_rollups.error_count + excluded.error_count,
			request_tokens = usage_rollups.request_tokens + excluded.request_tokens,
			response_tokens = usage_rollups.response_tokens + excluded.response_tokens,
			total_tokens = usage_rollups.total_tokens + excluded.total_tokens,
			key_name = excluded.key_name,
			owner = excluded.owner,
			purpose = excluded.purpose
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, rollup := range rollups {
		if _, err := stmt.ExecContext(
			ctx,
			rollup.BucketStart.Format(time.RFC3339),
			rollup.APIKey,
			rollup.KeyName,
			rollup.Owner,
			rollup.Purpose,
			rollup.Endpoint,
			rollup.Provider,
			rollup.PublicModel,
			rollup.UpstreamModel,
			rollup.RequestCount,
			rollup.SuccessCount,
			rollup.ErrorCount,
			rollup.RequestTokens,
			rollup.ResponseTokens,
			rollup.TotalTokens,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) UsageSummaries(ctx context.Context) ([]usage.Summary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			api_key,
			MAX(key_name),
			MAX(owner),
			MAX(purpose),
			COALESCE(SUM(request_count), 0),
			COALESCE(SUM(success_count), 0),
			COALESCE(SUM(error_count), 0),
			COALESCE(SUM(request_tokens), 0),
			COALESCE(SUM(response_tokens), 0),
			COALESCE(SUM(total_tokens), 0)
		FROM usage_rollups
		GROUP BY api_key
		ORDER BY api_key
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []usage.Summary
	for rows.Next() {
		var summary usage.Summary
		if err := rows.Scan(
			&summary.APIKey,
			&summary.KeyName,
			&summary.Owner,
			&summary.Purpose,
			&summary.RequestCount,
			&summary.SuccessCount,
			&summary.ErrorCount,
			&summary.RequestTokens,
			&summary.ResponseTokens,
			&summary.TotalTokens,
		); err != nil {
			return nil, err
		}
		out = append(out, summary)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *SQLiteStore) String() string {
	return fmt.Sprintf("sqlite(%p)", s.db)
}
