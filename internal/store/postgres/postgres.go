// Package postgres implements store.Store on jackc/pgx v5. Used in server mode.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/telecollection/telecollection/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the Postgres-backed store.Store implementation.
type Store struct {
	pool *pgxpool.Pool
}

var _ store.Store = (*Store)(nil)

// NOTE: inline schema bootstraps the foundation; subfase 0.7 (goose) supersedes it.
const schema = `
CREATE TABLE IF NOT EXISTS folders (
	id            BIGSERIAL PRIMARY KEY,
	tg_account_id BIGINT      NOT NULL,
	channel_id    BIGINT      NOT NULL,
	name          TEXT        NOT NULL,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);`

// Open connects a pool and ensures the schema exists.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	if _, err := pool.Exec(ctx, schema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: init schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Ping verifies connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// CreateFolder inserts a folder and returns it fully populated.
func (s *Store) CreateFolder(ctx context.Context, f store.Folder) (store.Folder, error) {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO folders (tg_account_id, channel_id, name) VALUES ($1, $2, $3) RETURNING id, created_at`,
		f.TGAccountID, f.ChannelID, f.Name).Scan(&f.ID, &f.CreatedAt)
	if err != nil {
		return store.Folder{}, fmt.Errorf("postgres: insert folder: %w", err)
	}
	return f, nil
}

// GetFolder returns the folder by ID or store.ErrNotFound.
func (s *Store) GetFolder(ctx context.Context, id int64) (store.Folder, error) {
	var f store.Folder
	err := s.pool.QueryRow(ctx,
		`SELECT id, tg_account_id, channel_id, name, created_at FROM folders WHERE id = $1`, id).
		Scan(&f.ID, &f.TGAccountID, &f.ChannelID, &f.Name, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Folder{}, store.ErrNotFound
	}
	if err != nil {
		return store.Folder{}, fmt.Errorf("postgres: get folder: %w", err)
	}
	return f, nil
}

// ListFolders returns folders for a Telegram account, ordered by ID.
func (s *Store) ListFolders(ctx context.Context, tgAccountID int64) ([]store.Folder, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, tg_account_id, channel_id, name, created_at FROM folders WHERE tg_account_id = $1 ORDER BY id`,
		tgAccountID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list folders: %w", err)
	}
	defer rows.Close()

	var out []store.Folder
	for rows.Next() {
		var f store.Folder
		if err := rows.Scan(&f.ID, &f.TGAccountID, &f.ChannelID, &f.Name, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan folder: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: rows: %w", err)
	}
	return out, nil
}

// Close releases the pool.
func (s *Store) Close() error {
	s.pool.Close()
	return nil
}
