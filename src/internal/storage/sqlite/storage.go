// Package sqlite provides SQLite-backed implementations of storage.Storage
// and storage.Logger. WAL mode is enabled for concurrency; a single *sql.DB
// connection pool is shared across both implementations.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"proxyllm/internal/domain"
)

// Open creates (or opens) the SQLite database at path, applies PRAGMA settings,
// runs migrations, and returns a ready-to-use *sql.DB.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite in WAL mode supports multiple concurrent readers. 
	// We allow a larger pool for reads, while SQLite handles write serialization internally.
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(time.Hour)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS model_nodes (
			id            TEXT PRIMARY KEY,
			name          TEXT NOT NULL DEFAULT '',
			aliases       TEXT NOT NULL DEFAULT '[]',
			base_url      TEXT NOT NULL,
			api_key       TEXT NOT NULL DEFAULT '',
			model_name    TEXT NOT NULL DEFAULT '',
			endpoint_type TEXT NOT NULL DEFAULT 'all',
			tpm           INTEGER NOT NULL DEFAULT 0,
			rpm           INTEGER NOT NULL DEFAULT 0,
			override      TEXT NOT NULL DEFAULT '{}',
			timeout_sec   INTEGER NOT NULL DEFAULT 120,
			enabled       INTEGER NOT NULL DEFAULT 1,
			created_at    TEXT NOT NULL,
			updated_at    TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id           TEXT PRIMARY KEY,
			key_value    TEXT NOT NULL UNIQUE,
			name         TEXT NOT NULL DEFAULT '',
			enabled      INTEGER NOT NULL DEFAULT 1,
			rate_limit   TEXT,
			allow_models TEXT NOT NULL DEFAULT '[]',
			created_at   TEXT NOT NULL,
			expires_at   TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS request_logs (
			id                 TEXT PRIMARY KEY,
			session_id         TEXT NOT NULL DEFAULT '',
			timestamp          TEXT NOT NULL,
			api_key_id         TEXT NOT NULL DEFAULT '',
			model_alias        TEXT NOT NULL DEFAULT '',
			node_id            TEXT NOT NULL DEFAULT '',
			actual_model       TEXT NOT NULL DEFAULT '',
			prompt_tokens      INTEGER NOT NULL DEFAULT 0,
			completion_tokens  INTEGER NOT NULL DEFAULT 0,
			total_tokens       INTEGER NOT NULL DEFAULT 0,
			duration_ms        INTEGER NOT NULL DEFAULT 0,
			status_code        INTEGER NOT NULL DEFAULT 0,
			stream             INTEGER NOT NULL DEFAULT 0,
			error_msg          TEXT NOT NULL DEFAULT '',
			has_detail         INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rl_timestamp   ON request_logs(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_rl_api_key_id  ON request_logs(api_key_id)`,
		`CREATE INDEX IF NOT EXISTS idx_rl_model_alias ON request_logs(model_alias)`,
		`CREATE INDEX IF NOT EXISTS idx_rl_session_id  ON request_logs(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_rl_node_id     ON request_logs(node_id)`,
		`CREATE TABLE IF NOT EXISTS detail_logs (
			trace_id      TEXT PRIMARY KEY,
			session_id    TEXT NOT NULL DEFAULT '',
			timestamp     TEXT NOT NULL,
			request_body  TEXT NOT NULL DEFAULT '',
			response_body TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_dl_session_id  ON detail_logs(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_dl_timestamp   ON detail_logs(timestamp)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Add columns if upgrading from older schema.
	_, _ = db.Exec(`ALTER TABLE model_nodes ADD COLUMN rate_limit TEXT`) // keep for legacy
	_, _ = db.Exec(`ALTER TABLE model_nodes ADD COLUMN tpm INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE model_nodes ADD COLUMN rpm INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE api_keys ADD COLUMN tpm INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE api_keys ADD COLUMN rpm INTEGER NOT NULL DEFAULT 0`)
	return nil
}

// ─── SQLiteStorage ────────────────────────────────────────────────────────────

type SQLiteStorage struct {
	db *sql.DB
}

func NewSQLiteStorage(db *sql.DB) *SQLiteStorage {
	return &SQLiteStorage{db: db}
}

// ── Nodes ──────────────────────────────────────────────────────────────────

func (s *SQLiteStorage) UpsertNode(ctx context.Context, n *domain.ModelNode) error {
	aliases, _ := json.Marshal(n.Aliases)
	override, _ := json.Marshal(n.Override)
	now := time.Now().UTC()
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO model_nodes
			(id,name,aliases,base_url,api_key,model_name,endpoint_type,override,timeout_sec,enabled,created_at,updated_at,tpm,rpm)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, aliases=excluded.aliases, base_url=excluded.base_url,
			api_key=excluded.api_key, model_name=excluded.model_name,
			endpoint_type=excluded.endpoint_type, override=excluded.override,
			timeout_sec=excluded.timeout_sec, enabled=excluded.enabled,
			updated_at=excluded.updated_at, tpm=excluded.tpm, rpm=excluded.rpm`,
		n.ID, n.Name, string(aliases), n.BaseURL, n.APIKey, n.ModelName,
		string(n.EndpointType), string(override),
		n.TimeoutSec, boolToInt(n.Enabled),
		n.CreatedAt.Format(time.RFC3339Nano), n.UpdatedAt.Format(time.RFC3339Nano),
		n.TPM, n.RPM,
	)
	return err
}

func (s *SQLiteStorage) GetNode(ctx context.Context, id string) (*domain.ModelNode, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, aliases, base_url, api_key, model_name, endpoint_type, tpm, rpm, override, timeout_sec, enabled, created_at, updated_at FROM model_nodes WHERE id=?`, id)
	return scanNode(row)
}

func (s *SQLiteStorage) ListNodes(ctx context.Context) ([]*domain.ModelNode, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, aliases, base_url, api_key, model_name, endpoint_type, tpm, rpm, override, timeout_sec, enabled, created_at, updated_at FROM model_nodes ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.ModelNode
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *SQLiteStorage) DeleteNode(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM model_nodes WHERE id=?`, id)
	return err
}

// ── API Keys ───────────────────────────────────────────────────────────────

func (s *SQLiteStorage) UpsertAPIKey(ctx context.Context, k *domain.APIKey) error {
	allowModels, _ := json.Marshal(k.AllowModels)
	var expiresAt *string
	if k.ExpiresAt != nil {
		str := k.ExpiresAt.UTC().Format(time.RFC3339Nano)
		expiresAt = &str
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO api_keys (id,key_value,name,enabled,tpm,rpm,allow_models,created_at,expires_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			key_value=excluded.key_value, name=excluded.name, enabled=excluded.enabled,
			tpm=excluded.tpm, rpm=excluded.rpm, allow_models=excluded.allow_models,
			expires_at=excluded.expires_at`,
		k.ID, k.Key, k.Name, boolToInt(k.Enabled), k.TPM, k.RPM, string(allowModels),
		k.CreatedAt.UTC().Format(time.RFC3339Nano), expiresAt,
	)
	return err
}

func (s *SQLiteStorage) GetAPIKey(ctx context.Context, id string) (*domain.APIKey, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, key_value, name, enabled, tpm, rpm, allow_models, created_at, expires_at FROM api_keys WHERE id=?`, id)
	return scanAPIKey(row)
}

func (s *SQLiteStorage) GetAPIKeyByValue(ctx context.Context, keyValue string) (*domain.APIKey, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, key_value, name, enabled, tpm, rpm, allow_models, created_at, expires_at FROM api_keys WHERE key_value=?`, keyValue)
	return scanAPIKey(row)
}

func (s *SQLiteStorage) ListAPIKeys(ctx context.Context) ([]*domain.APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, key_value, name, enabled, tpm, rpm, allow_models, created_at, expires_at FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *SQLiteStorage) DeleteAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id=?`, id)
	return err
}

func (s *SQLiteStorage) Close() error { return s.db.Close() }

// ─── Scan helpers ─────────────────────────────────────────────────────────────

type scanner interface {
	Scan(dest ...any) error
}

func scanNode(s scanner) (*domain.ModelNode, error) {
	var n domain.ModelNode
	var aliases, override, endpointType, createdAt, updatedAt string
	var enabled int
	err := s.Scan(
		&n.ID, &n.Name, &aliases, &n.BaseURL, &n.APIKey, &n.ModelName,
		&endpointType, &n.TPM, &n.RPM, &override,
		&n.TimeoutSec, &enabled, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n.Enabled = enabled != 0
	n.EndpointType = domain.EndpointType(endpointType)
	_ = json.Unmarshal([]byte(aliases), &n.Aliases)
	_ = json.Unmarshal([]byte(override), &n.Override)
	n.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	n.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &n, nil
}

func scanAPIKey(s scanner) (*domain.APIKey, error) {
	var k domain.APIKey
	var enabled int
	var expiresAt sql.NullString
	var allowModels, createdAt string
	err := s.Scan(
		&k.ID, &k.Key, &k.Name, &enabled, &k.TPM, &k.RPM, &allowModels, &createdAt, &expiresAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	k.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(allowModels), &k.AllowModels)
	k.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if expiresAt.Valid && expiresAt.String != "" {
		t, _ := time.Parse(time.RFC3339Nano, expiresAt.String)
		k.ExpiresAt = &t
	}
	return &k, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
