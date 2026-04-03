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
			api_key TEXT NOT NULL DEFAULT '',
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
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS usage_rollups (
			bucket_start TEXT NOT NULL,
			key_id TEXT NOT NULL,
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
			PRIMARY KEY (bucket_start, key_id, endpoint, provider, public_model, upstream_model)
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
	if err := s.migrateAuthKeysTable(ctx); err != nil {
		return err
	}
	return s.migrateUsageRollupsTable(ctx)
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
	if hasAPIKey && hasAPIKeyRef {
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
			api_key TEXT NOT NULL DEFAULT '',
			base_url TEXT NOT NULL,
			api_key_ref TEXT NOT NULL DEFAULT '',
			timeout_seconds INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`DROP TABLE providers_old`,
	}
	if hasAPIKey && !hasAPIKeyRef {
		stmts = []string{
			stmts[0],
			stmts[1],
			`INSERT INTO providers(name, api_key, base_url, api_key_ref, timeout_seconds, updated_at)
			 SELECT name, api_key, base_url, '', timeout_seconds, updated_at FROM providers_old`,
			stmts[2],
		}
	}
	if !hasAPIKey && hasAPIKeyRef {
		stmts = []string{
			stmts[0],
			stmts[1],
			`INSERT INTO providers(name, api_key, base_url, api_key_ref, timeout_seconds, updated_at)
			 SELECT name, '', base_url, api_key_ref, timeout_seconds, updated_at FROM providers_old`,
			stmts[2],
		}
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

func (s *SQLiteStore) migrateUsageRollupsTable(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(usage_rollups)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var hasAPIKey bool
	var hasKeyID bool
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
		case "key_id":
			hasKeyID = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hasKeyID || !hasAPIKey {
		return nil
	}

	type legacyRollup struct {
		BucketStart    string
		APIKey         string
		KeyName        string
		Owner          string
		Purpose        string
		Endpoint       string
		Provider       string
		PublicModel    string
		UpstreamModel  string
		RequestCount   int64
		SuccessCount   int64
		ErrorCount     int64
		RequestTokens  int64
		ResponseTokens int64
		TotalTokens    int64
	}

	legacyRows, err := s.db.QueryContext(ctx, `
		SELECT bucket_start, api_key, key_name, owner, purpose, endpoint, provider, public_model, upstream_model,
		       request_count, success_count, error_count, request_tokens, response_tokens, total_tokens
		FROM usage_rollups
	`)
	if err != nil {
		return err
	}
	var legacy []legacyRollup
	for legacyRows.Next() {
		var item legacyRollup
		if err := legacyRows.Scan(
			&item.BucketStart,
			&item.APIKey,
			&item.KeyName,
			&item.Owner,
			&item.Purpose,
			&item.Endpoint,
			&item.Provider,
			&item.PublicModel,
			&item.UpstreamModel,
			&item.RequestCount,
			&item.SuccessCount,
			&item.ErrorCount,
			&item.RequestTokens,
			&item.ResponseTokens,
			&item.TotalTokens,
		); err != nil {
			legacyRows.Close()
			return err
		}
		legacy = append(legacy, item)
	}
	if err := legacyRows.Err(); err != nil {
		legacyRows.Close()
		return err
	}
	legacyRows.Close()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `ALTER TABLE usage_rollups RENAME TO usage_rollups_old`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE usage_rollups (
			bucket_start TEXT NOT NULL,
			key_id TEXT NOT NULL,
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
			PRIMARY KEY (bucket_start, key_id, endpoint, provider, public_model, upstream_model)
		)`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_rollups(
			bucket_start, key_id, key_name, owner, purpose, endpoint, provider, public_model, upstream_model,
			request_count, success_count, error_count, request_tokens, response_tokens, total_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, item := range legacy {
		if _, err := stmt.ExecContext(
			ctx,
			item.BucketStart,
			usage.KeyID(item.APIKey),
			item.KeyName,
			item.Owner,
			item.Purpose,
			item.Endpoint,
			item.Provider,
			item.PublicModel,
			item.UpstreamModel,
			item.RequestCount,
			item.SuccessCount,
			item.ErrorCount,
			item.RequestTokens,
			item.ResponseTokens,
			item.TotalTokens,
		); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE usage_rollups_old`); err != nil {
		return err
	}
	return tx.Commit()
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
	rows, err := s.db.QueryContext(ctx, `SELECT name, api_key, base_url, api_key_ref, timeout_seconds FROM providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []config.ProviderConfig
	for rows.Next() {
		var provider config.ProviderConfig
		if err := rows.Scan(&provider.Name, &provider.APIKey, &provider.BaseURL, &provider.APIKeyRef, &provider.TimeoutSeconds); err != nil {
			return nil, err
		}
		out = append(out, provider)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetProvider(ctx context.Context, name string) (config.ProviderConfig, error) {
	var provider config.ProviderConfig
	err := s.db.QueryRowContext(ctx, `SELECT name, api_key, base_url, api_key_ref, timeout_seconds FROM providers WHERE name = ?`, name).
		Scan(&provider.Name, &provider.APIKey, &provider.BaseURL, &provider.APIKeyRef, &provider.TimeoutSeconds)
	if err != nil {
		if err == sql.ErrNoRows {
			return config.ProviderConfig{}, fmt.Errorf("provider %q not found", name)
		}
		return config.ProviderConfig{}, err
	}
	return provider, nil
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
	rows, err := s.db.QueryContext(ctx, `SELECT key, name, owner, purpose FROM auth_keys ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []config.KeyConfig
	for rows.Next() {
		var item config.KeyConfig
		if err := rows.Scan(&item.Key, &item.Name, &item.Owner, &item.Purpose); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpsertProvider(ctx context.Context, provider config.ProviderConfig) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO providers(name, api_key, base_url, api_key_ref, timeout_seconds, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			api_key = excluded.api_key,
			base_url = excluded.base_url,
			api_key_ref = excluded.api_key_ref,
			timeout_seconds = excluded.timeout_seconds,
			updated_at = excluded.updated_at
	`, provider.Name, provider.APIKey, provider.BaseURL, provider.APIKeyRef, provider.TimeoutSeconds, time.Now().UTC().Format(time.RFC3339))
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_keys(key, name, owner, purpose, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			name = excluded.name,
			owner = excluded.owner,
			purpose = excluded.purpose,
			updated_at = excluded.updated_at
	`, key.Key, key.Name, key.Owner, key.Purpose, time.Now().UTC().Format(time.RFC3339))
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
			bucket_start, key_id, key_name, owner, purpose, endpoint, provider, public_model, upstream_model,
			request_count, success_count, error_count, request_tokens, response_tokens, total_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bucket_start, key_id, endpoint, provider, public_model, upstream_model) DO UPDATE SET
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
			rollup.KeyID,
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
			key_id,
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
		GROUP BY key_id
		ORDER BY key_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []usage.Summary
	for rows.Next() {
		var summary usage.Summary
		if err := rows.Scan(
			&summary.KeyID,
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
