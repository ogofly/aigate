package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"llmgate/internal/config"
	"llmgate/internal/usage"
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
			anthropic_base_url TEXT NOT NULL DEFAULT '',
			anthropic_version TEXT NOT NULL DEFAULT '',
			api_key_ref TEXT NOT NULL,
			timeout_seconds INTEGER NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS models (
			id TEXT PRIMARY KEY,
			public_name TEXT NOT NULL,
			provider TEXT NOT NULL,
			upstream_name TEXT NOT NULL,
			priority INTEGER NOT NULL DEFAULT 0,
			weight INTEGER NOT NULL DEFAULT 1,
			enabled INTEGER NOT NULL DEFAULT 1,
			updated_at TEXT NOT NULL
			, UNIQUE(public_name, provider, upstream_name)
		)`,
		`CREATE TABLE IF NOT EXISTS auth_keys (
			key TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			owner TEXT NOT NULL DEFAULT '',
			purpose TEXT NOT NULL DEFAULT '',
			model_access TEXT NOT NULL DEFAULT 'all',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS auth_key_model_routes (
			key TEXT NOT NULL,
			model_route_id TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (key, model_route_id)
		)`,
		`CREATE TABLE IF NOT EXISTS routing_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			selection TEXT NOT NULL DEFAULT 'priority',
			failover_enabled INTEGER NOT NULL DEFAULT 1,
			failover_max_attempts INTEGER NOT NULL DEFAULT 2,
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
	if err := s.migrateProvidersAnthropicColumns(ctx); err != nil {
		return err
	}
	if err := s.migrateProvidersEnabledColumn(ctx); err != nil {
		return err
	}
	if err := s.migrateModelsRoutesTable(ctx); err != nil {
		return err
	}
	if err := s.migrateAuthKeysTable(ctx); err != nil {
		return err
	}
	if err := s.migrateRoutingSettings(ctx); err != nil {
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
			anthropic_base_url TEXT NOT NULL DEFAULT '',
			anthropic_version TEXT NOT NULL DEFAULT '',
			api_key_ref TEXT NOT NULL DEFAULT '',
			timeout_seconds INTEGER NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			updated_at TEXT NOT NULL
		)`,
		`DROP TABLE providers_old`,
	}
	if hasAPIKey && !hasAPIKeyRef {
		stmts = []string{
			stmts[0],
			stmts[1],
			`INSERT INTO providers(name, api_key, base_url, anthropic_base_url, anthropic_version, api_key_ref, timeout_seconds, enabled, updated_at)
			 SELECT name, api_key, base_url, '', '', '', timeout_seconds, 1, updated_at FROM providers_old`,
			stmts[2],
		}
	}
	if !hasAPIKey && hasAPIKeyRef {
		stmts = []string{
			stmts[0],
			stmts[1],
			`INSERT INTO providers(name, api_key, base_url, anthropic_base_url, anthropic_version, api_key_ref, timeout_seconds, enabled, updated_at)
			 SELECT name, '', base_url, '', '', api_key_ref, timeout_seconds, 1, updated_at FROM providers_old`,
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

func (s *SQLiteStore) migrateProvidersAnthropicColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(providers)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var hasAnthropicBaseURL bool
	var hasAnthropicVersion bool
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
		case "anthropic_base_url":
			hasAnthropicBaseURL = true
		case "anthropic_version":
			hasAnthropicVersion = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if !hasAnthropicBaseURL {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE providers ADD COLUMN anthropic_base_url TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !hasAnthropicVersion {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE providers ADD COLUMN anthropic_version TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) migrateProvidersEnabledColumn(ctx context.Context) error {
	hasEnabled, err := s.tableHasColumn(ctx, "providers", "enabled")
	if err != nil {
		return err
	}
	if hasEnabled {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE providers ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`)
	return err
}

func (s *SQLiteStore) migrateModelsRoutesTable(ctx context.Context) error {
	hasID, err := s.tableHasColumn(ctx, "models", "id")
	if err != nil {
		return err
	}
	hasEnabled, err := s.tableHasColumn(ctx, "models", "enabled")
	if err != nil {
		return err
	}
	if hasID && hasEnabled {
		return nil
	}

	type legacyModel struct {
		PublicName   string
		Provider     string
		UpstreamName string
		UpdatedAt    string
	}
	rows, err := s.db.QueryContext(ctx, `SELECT public_name, provider, upstream_name, updated_at FROM models`)
	if err != nil {
		return err
	}
	var legacy []legacyModel
	for rows.Next() {
		var item legacyModel
		if err := rows.Scan(&item.PublicName, &item.Provider, &item.UpstreamName, &item.UpdatedAt); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `ALTER TABLE models RENAME TO models_old`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE models (
			id TEXT PRIMARY KEY,
			public_name TEXT NOT NULL,
			provider TEXT NOT NULL,
			upstream_name TEXT NOT NULL,
			priority INTEGER NOT NULL DEFAULT 0,
			weight INTEGER NOT NULL DEFAULT 1,
			enabled INTEGER NOT NULL DEFAULT 1,
			updated_at TEXT NOT NULL,
			UNIQUE(public_name, provider, upstream_name)
		)`); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO models(id, public_name, provider, upstream_name, priority, weight, enabled, updated_at)
		VALUES (?, ?, ?, ?, 0, 1, 1, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, item := range legacy {
		updatedAt := item.UpdatedAt
		if updatedAt == "" {
			updatedAt = now
		}
		if _, err := stmt.ExecContext(ctx, newModelRouteID(), item.PublicName, item.Provider, item.UpstreamName, updatedAt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE models_old`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) migrateAuthKeysTable(ctx context.Context) error {
	hasModelAccess, err := s.tableHasColumn(ctx, "auth_keys", "model_access")
	if err != nil {
		return err
	}
	if !hasModelAccess {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE auth_keys ADD COLUMN model_access TEXT NOT NULL DEFAULT 'all'`); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) migrateRoutingSettings(ctx context.Context) error {
	settings := config.RoutingConfig{Selection: "priority", FailoverEnabled: true, FailoverMaxAttempts: 2}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO routing_settings(id, selection, failover_enabled, failover_max_attempts, updated_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, settings.Selection, boolToInt(settings.FailoverEnabled), settings.FailoverMaxAttempts, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) tableHasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
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
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
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
		if !provider.Enabled {
			provider.Enabled = true
		}
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
		if !model.Enabled {
			model.Enabled = true
		}
		if model.Weight <= 0 {
			model.Weight = 1
		}
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
	rows, err := s.db.QueryContext(ctx, `SELECT name, api_key, base_url, anthropic_base_url, anthropic_version, api_key_ref, timeout_seconds, enabled FROM providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []config.ProviderConfig
	for rows.Next() {
		var provider config.ProviderConfig
		var enabled int
		if err := rows.Scan(&provider.Name, &provider.APIKey, &provider.BaseURL, &provider.AnthropicBaseURL, &provider.AnthropicVersion, &provider.APIKeyRef, &provider.TimeoutSeconds, &enabled); err != nil {
			return nil, err
		}
		provider.Enabled = enabled != 0
		out = append(out, provider)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetProvider(ctx context.Context, name string) (config.ProviderConfig, error) {
	var provider config.ProviderConfig
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT name, api_key, base_url, anthropic_base_url, anthropic_version, api_key_ref, timeout_seconds, enabled FROM providers WHERE name = ?`, name).
		Scan(&provider.Name, &provider.APIKey, &provider.BaseURL, &provider.AnthropicBaseURL, &provider.AnthropicVersion, &provider.APIKeyRef, &provider.TimeoutSeconds, &enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return config.ProviderConfig{}, fmt.Errorf("provider %q not found", name)
		}
		return config.ProviderConfig{}, err
	}
	provider.Enabled = enabled != 0
	return provider, nil
}

func (s *SQLiteStore) ListModels(ctx context.Context) ([]config.ModelConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, public_name, provider, upstream_name, priority, weight, enabled FROM models ORDER BY public_name, priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []config.ModelConfig
	for rows.Next() {
		var model config.ModelConfig
		var enabled int
		if err := rows.Scan(&model.ID, &model.PublicName, &model.Provider, &model.UpstreamName, &model.Priority, &model.Weight, &enabled); err != nil {
			return nil, err
		}
		model.Enabled = enabled != 0
		out = append(out, model)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListAuthKeys(ctx context.Context) ([]config.KeyConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, name, owner, purpose, model_access FROM auth_keys ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []config.KeyConfig
	for rows.Next() {
		var item config.KeyConfig
		if err := rows.Scan(&item.Key, &item.Name, &item.Owner, &item.Purpose, &item.ModelAccess); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		if err := s.populateKeyRoutes(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SQLiteStore) ListAuthKeysByOwner(ctx context.Context, owner string) ([]config.KeyConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, name, owner, purpose, model_access FROM auth_keys WHERE owner = ? ORDER BY key`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []config.KeyConfig
	for rows.Next() {
		var item config.KeyConfig
		if err := rows.Scan(&item.Key, &item.Name, &item.Owner, &item.Purpose, &item.ModelAccess); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		if err := s.populateKeyRoutes(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SQLiteStore) GetAuthKey(ctx context.Context, key string) (config.KeyConfig, error) {
	var item config.KeyConfig
	err := s.db.QueryRowContext(ctx, `SELECT key, name, owner, purpose, model_access FROM auth_keys WHERE key = ?`, key).
		Scan(&item.Key, &item.Name, &item.Owner, &item.Purpose, &item.ModelAccess)
	if err != nil {
		if err == sql.ErrNoRows {
			return config.KeyConfig{}, fmt.Errorf("auth key not found")
		}
		return config.KeyConfig{}, err
	}
	if err := s.populateKeyRoutes(ctx, &item); err != nil {
		return config.KeyConfig{}, err
	}
	return item, nil
}

func (s *SQLiteStore) UpsertProvider(ctx context.Context, provider config.ProviderConfig) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO providers(name, api_key, base_url, anthropic_base_url, anthropic_version, api_key_ref, timeout_seconds, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			api_key = excluded.api_key,
			base_url = excluded.base_url,
			anthropic_base_url = excluded.anthropic_base_url,
			anthropic_version = excluded.anthropic_version,
			api_key_ref = excluded.api_key_ref,
			timeout_seconds = excluded.timeout_seconds,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, provider.Name, provider.APIKey, provider.BaseURL, provider.AnthropicBaseURL, provider.AnthropicVersion, provider.APIKeyRef, provider.TimeoutSeconds, boolToInt(provider.Enabled), time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) UpsertModel(ctx context.Context, model config.ModelConfig) error {
	if strings.TrimSpace(model.ID) == "" {
		model.ID = newModelRouteID()
	}
	model.SetDefaults()
	if exists, err := s.modelRouteIDExists(ctx, model.ID); err != nil {
		return err
	} else if exists {
		_, err := s.db.ExecContext(ctx, `
			UPDATE models
			SET public_name = ?, provider = ?, upstream_name = ?, priority = ?, weight = ?, enabled = ?, updated_at = ?
			WHERE id = ?
		`, model.PublicName, model.Provider, model.UpstreamName, model.Priority, model.Weight, boolToInt(model.Enabled), time.Now().UTC().Format(time.RFC3339), model.ID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO models(id, public_name, provider, upstream_name, priority, weight, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(public_name, provider, upstream_name) DO UPDATE SET
			priority = excluded.priority,
			weight = excluded.weight,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at
	`, model.ID, model.PublicName, model.Provider, model.UpstreamName, model.Priority, model.Weight, boolToInt(model.Enabled), time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) DeleteProvider(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE name = ?`, name)
	return err
}

func (s *SQLiteStore) DeleteModel(ctx context.Context, publicName string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var routeID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM models WHERE id = ?`, publicName).Scan(&routeID)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM models WHERE public_name = ?`, publicName)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if routeID != "" {
				rows.Close()
				return fmt.Errorf("model %q has multiple routes; use model route id", publicName)
			}
			routeID = id
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	if routeID == "" {
		return tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_key_model_routes WHERE model_route_id = ?`, routeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM models WHERE id = ?`, routeID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) UpsertAuthKey(ctx context.Context, key config.KeyConfig) error {
	key.SetDefaults()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO auth_keys(key, name, owner, purpose, model_access, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			name = excluded.name,
			owner = excluded.owner,
			purpose = excluded.purpose,
			model_access = excluded.model_access,
			updated_at = excluded.updated_at
	`, key.Key, key.Name, key.Owner, key.Purpose, key.ModelAccess, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_key_model_routes WHERE key = ?`, key.Key); err != nil {
		return err
	}
	if strings.EqualFold(key.ModelAccess, "selected") {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO auth_key_model_routes(key, model_route_id, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT(key, model_route_id) DO NOTHING
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		now := time.Now().UTC().Format(time.RFC3339)
		for _, routeID := range key.ModelRouteIDs {
			routeID = strings.TrimSpace(routeID)
			if routeID == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, key.Key, routeID, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) DeleteAuthKey(ctx context.Context, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_key_model_routes WHERE key = ?`, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_keys WHERE key = ?`, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) GetRoutingSettings(ctx context.Context) (config.RoutingConfig, error) {
	settings := config.RoutingConfig{Selection: "priority", FailoverEnabled: true, FailoverMaxAttempts: 2}
	var failoverEnabled int
	err := s.db.QueryRowContext(ctx, `SELECT selection, failover_enabled, failover_max_attempts FROM routing_settings WHERE id = 1`).
		Scan(&settings.Selection, &failoverEnabled, &settings.FailoverMaxAttempts)
	if err != nil {
		if err == sql.ErrNoRows {
			return settings, nil
		}
		return config.RoutingConfig{}, err
	}
	settings.FailoverEnabled = failoverEnabled != 0
	if settings.FailoverMaxAttempts <= 0 {
		settings.FailoverMaxAttempts = 2
	}
	if settings.Selection != "priority" && settings.Selection != "weight" && settings.Selection != "random" {
		settings.Selection = "priority"
	}
	return settings, nil
}

func (s *SQLiteStore) UpsertRoutingSettings(ctx context.Context, settings config.RoutingConfig) error {
	if settings.Selection != "priority" && settings.Selection != "weight" && settings.Selection != "random" {
		settings.Selection = "priority"
	}
	if settings.FailoverMaxAttempts <= 0 {
		settings.FailoverMaxAttempts = 2
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO routing_settings(id, selection, failover_enabled, failover_max_attempts, updated_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			selection = excluded.selection,
			failover_enabled = excluded.failover_enabled,
			failover_max_attempts = excluded.failover_max_attempts,
			updated_at = excluded.updated_at
	`, settings.Selection, boolToInt(settings.FailoverEnabled), settings.FailoverMaxAttempts, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *SQLiteStore) populateKeyRoutes(ctx context.Context, key *config.KeyConfig) error {
	if key == nil {
		return nil
	}
	key.SetDefaults()
	rows, err := s.db.QueryContext(ctx, `SELECT model_route_id FROM auth_key_model_routes WHERE key = ? ORDER BY model_route_id`, key.Key)
	if err != nil {
		return err
	}
	defer rows.Close()
	key.ModelRouteIDs = nil
	for rows.Next() {
		var routeID string
		if err := rows.Scan(&routeID); err != nil {
			return err
		}
		key.ModelRouteIDs = append(key.ModelRouteIDs, routeID)
	}
	return rows.Err()
}

func (s *SQLiteStore) modelRouteIDExists(ctx context.Context, id string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM models WHERE id = ?`, id).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func newModelRouteID() string {
	return "mrt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
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

func (s *SQLiteStore) QueryUsageRollups(ctx context.Context, filter UsageFilter) ([]usage.Rollup, error) {
	query := `
		SELECT
			bucket_start,
			key_id,
			key_name,
			owner,
			purpose,
			endpoint,
			provider,
			public_model,
			upstream_model,
			request_count,
			success_count,
			error_count,
			request_tokens,
			response_tokens,
			total_tokens
		FROM usage_rollups
		WHERE 1=1`
	args := []any{}
	query, args = appendUsageFilter(query, args, filter)
	query += " ORDER BY bucket_start, key_id, endpoint, provider, public_model, upstream_model"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []usage.Rollup
	for rows.Next() {
		var (
			rollup      usage.Rollup
			bucketStart string
		)
		if err := rows.Scan(
			&bucketStart,
			&rollup.KeyID,
			&rollup.KeyName,
			&rollup.Owner,
			&rollup.Purpose,
			&rollup.Endpoint,
			&rollup.Provider,
			&rollup.PublicModel,
			&rollup.UpstreamModel,
			&rollup.RequestCount,
			&rollup.SuccessCount,
			&rollup.ErrorCount,
			&rollup.RequestTokens,
			&rollup.ResponseTokens,
			&rollup.TotalTokens,
		); err != nil {
			return nil, err
		}
		rollup.BucketStart, err = time.Parse(time.RFC3339, bucketStart)
		if err != nil {
			return nil, err
		}
		out = append(out, rollup)
	}
	return out, rows.Err()
}

type TrendPoint struct {
	Date           string `json:"date"`
	RequestCount   int64  `json:"request_count"`
	SuccessCount   int64  `json:"success_count"`
	ErrorCount     int64  `json:"error_count"`
	RequestTokens  int64  `json:"request_tokens"`
	ResponseTokens int64  `json:"response_tokens"`
	TotalTokens    int64  `json:"total_tokens"`
}

func (s *SQLiteStore) QueryUsageTrend(ctx context.Context, filter UsageFilter, groupBy string) ([]TrendPoint, error) {
	query := `
		SELECT
			bucket_start,
			COALESCE(SUM(request_count), 0),
			COALESCE(SUM(success_count), 0),
			COALESCE(SUM(error_count), 0),
			COALESCE(SUM(request_tokens), 0),
			COALESCE(SUM(response_tokens), 0),
			COALESCE(SUM(total_tokens), 0)
		FROM usage_rollups
		WHERE 1=1`
	args := []any{}
	query, args = appendUsageFilter(query, args, filter)
	query += " GROUP BY bucket_start ORDER BY 1"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Convert UTC bucket_start to local time and group
	type partial struct {
		bucket         string
		requestCount   int64
		successCount   int64
		errorCount     int64
		requestTokens  int64
		responseTokens int64
		totalTokens    int64
	}
	var buckets []partial
	for rows.Next() {
		var p partial
		if err := rows.Scan(
			&p.bucket,
			&p.requestCount,
			&p.successCount,
			&p.errorCount,
			&p.requestTokens,
			&p.responseTokens,
			&p.totalTokens,
		); err != nil {
			return nil, err
		}
		buckets = append(buckets, p)
	}

	type agg struct {
		requestCount, successCount, errorCount, requestTokens, responseTokens, totalTokens int64
	}
	orderedKeys := make([]string, 0)
	groups := make(map[string]*agg)
	for _, p := range buckets {
		t, err := time.Parse(time.RFC3339, p.bucket)
		if err != nil {
			continue
		}
		local := t.In(time.Local)
		var key string
		if groupBy == "hour" {
			key = local.Format("2006-01-02 15:00")
		} else {
			key = local.Format("2006-01-02")
		}
		if _, ok := groups[key]; !ok {
			groups[key] = &agg{}
			orderedKeys = append(orderedKeys, key)
		}
		g := groups[key]
		g.requestCount += p.requestCount
		g.successCount += p.successCount
		g.errorCount += p.errorCount
		g.requestTokens += p.requestTokens
		g.responseTokens += p.responseTokens
		g.totalTokens += p.totalTokens
	}

	out := make([]TrendPoint, 0, len(groups))
	for _, key := range orderedKeys {
		g := groups[key]
		out = append(out, TrendPoint{
			Date:           key,
			RequestCount:   g.requestCount,
			SuccessCount:   g.successCount,
			ErrorCount:     g.errorCount,
			RequestTokens:  g.requestTokens,
			ResponseTokens: g.responseTokens,
			TotalTokens:    g.totalTokens,
		})
	}
	return out, nil
}

type UsageFilter struct {
	StartTime time.Time
	EndTime   time.Time
	KeyID     string
	Model     string
	Provider  string
	Owner     string
}

func (s *SQLiteStore) QueryUsage(ctx context.Context, filter UsageFilter) ([]usage.Summary, error) {
	query := `
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
		WHERE 1=1`
	args := []any{}
	query, args = appendUsageFilter(query, args, filter)
	query += " GROUP BY key_id ORDER BY key_id"

	rows, err := s.db.QueryContext(ctx, query, args...)
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

func (s *SQLiteStore) ListUsageModels(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT public_model FROM usage_rollups WHERE public_model != '' ORDER BY public_model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

func (s *SQLiteStore) ListUsageProviders(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT provider FROM usage_rollups WHERE provider != '' ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []string
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, rows.Err()
}

func (s *SQLiteStore) QueryUsageByModel(ctx context.Context, filter UsageFilter) ([]usage.ModelSummary, error) {
	query := `
		SELECT
			public_model,
			COALESCE(SUM(request_count), 0),
			COALESCE(SUM(success_count), 0),
			COALESCE(SUM(error_count), 0),
			COALESCE(SUM(request_tokens), 0),
			COALESCE(SUM(response_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COUNT(DISTINCT key_id)
		FROM usage_rollups
		WHERE public_model != ''`
	args := []any{}
	query, args = appendUsageFilter(query, args, filter)
	query += " GROUP BY public_model ORDER BY request_count DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []usage.ModelSummary
	for rows.Next() {
		var m usage.ModelSummary
		if err := rows.Scan(
			&m.Model,
			&m.RequestCount,
			&m.SuccessCount,
			&m.ErrorCount,
			&m.RequestTokens,
			&m.ResponseTokens,
			&m.TotalTokens,
			&m.KeyCount,
		); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func appendUsageFilter(query string, args []any, filter UsageFilter) (string, []any) {
	if filter.KeyID != "" {
		query += " AND key_id = ?"
		args = append(args, filter.KeyID)
	}
	if !filter.StartTime.IsZero() {
		query += " AND bucket_start >= ?"
		args = append(args, filter.StartTime.UTC().Format(time.RFC3339))
	}
	if !filter.EndTime.IsZero() {
		query += " AND bucket_start < ?"
		args = append(args, filter.EndTime.UTC().Add(24*time.Hour).Format(time.RFC3339))
	}
	if filter.Model != "" {
		query += " AND public_model = ?"
		args = append(args, filter.Model)
	}
	if filter.Provider != "" {
		query += " AND provider = ?"
		args = append(args, filter.Provider)
	}
	if filter.Owner != "" {
		query += " AND owner = ?"
		args = append(args, filter.Owner)
	}
	return query, args
}

func (s *SQLiteStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *SQLiteStore) String() string {
	return fmt.Sprintf("sqlite(%p)", s.db)
}
