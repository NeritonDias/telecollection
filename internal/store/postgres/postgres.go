// Package postgres implements store.Store on jackc/pgx v5. Used in server mode.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/telecollection/telecollection/internal/store"
	"github.com/telecollection/telecollection/internal/store/migrate"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver for migrations
)

// Store is the Postgres-backed store.Store implementation.
type Store struct {
	pool *pgxpool.Pool
}

var _ store.Store = (*Store)(nil)

// Open connects a pool and applies pending migrations. The schema lives in
// internal/store/migrate/postgres (goose) as the single source of truth; goose
// needs a database/sql handle, so migrations run over a short-lived stdlib
// connection before the pgx pool takes over.
func Open(ctx context.Context, dsn string) (*Store, error) {
	if err := runMigrations(dsn); err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	return &Store{pool: pool}, nil
}

func runMigrations(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("postgres: open migration conn: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := migrate.UpPostgres(db); err != nil {
		return fmt.Errorf("postgres: migrate: %w", err)
	}
	return nil
}

// Ping verifies connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// CreateFolder inserts a folder or returns the existing row keyed by
// (tg_account_id, channel_id), returning it fully populated. The upsert makes
// concurrent creates for the same channel converge on a single row instead of
// duplicating. CreatedAt is preserved on the conflict path.
func (s *Store) CreateFolder(ctx context.Context, f store.Folder) (store.Folder, error) {
	var out store.Folder
	err := s.pool.QueryRow(ctx,
		`INSERT INTO folders (tg_account_id, channel_id, name)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (tg_account_id, channel_id) DO UPDATE SET
		   name = EXCLUDED.name
		 RETURNING id, tg_account_id, channel_id, name, created_at`,
		f.TGAccountID, f.ChannelID, f.Name).
		Scan(&out.ID, &out.TGAccountID, &out.ChannelID, &out.Name, &out.CreatedAt)
	if err != nil {
		return store.Folder{}, fmt.Errorf("postgres: upsert folder: %w", err)
	}
	return out, nil
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

// UpsertFile inserts a file or updates the existing row keyed by
// (folder_id, message_id), returning it fully populated. CreatedAt is preserved
// on update.
func (s *Store) UpsertFile(ctx context.Context, f store.File) (store.File, error) {
	var out store.File
	err := s.pool.QueryRow(ctx,
		`INSERT INTO files (folder_id, message_id, name, size, mime)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (folder_id, message_id) DO UPDATE SET
		   name = EXCLUDED.name,
		   size = EXCLUDED.size,
		   mime = EXCLUDED.mime
		 RETURNING id, folder_id, message_id, name, size, mime, created_at`,
		f.FolderID, f.MessageID, f.Name, f.Size, f.MIME).
		Scan(&out.ID, &out.FolderID, &out.MessageID, &out.Name, &out.Size, &out.MIME, &out.CreatedAt)
	if err != nil {
		return store.File{}, fmt.Errorf("postgres: upsert file: %w", err)
	}
	return out, nil
}

// GetFile returns the file by ID or store.ErrNotFound.
func (s *Store) GetFile(ctx context.Context, id int64) (store.File, error) {
	var f store.File
	err := s.pool.QueryRow(ctx,
		`SELECT id, folder_id, message_id, name, size, mime, created_at FROM files WHERE id = $1`, id).
		Scan(&f.ID, &f.FolderID, &f.MessageID, &f.Name, &f.Size, &f.MIME, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.File{}, store.ErrNotFound
	}
	if err != nil {
		return store.File{}, fmt.Errorf("postgres: get file: %w", err)
	}
	return f, nil
}

// ListFiles returns files for a folder, ordered by ID.
func (s *Store) ListFiles(ctx context.Context, folderID int64) ([]store.File, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, folder_id, message_id, name, size, mime, created_at FROM files WHERE folder_id = $1 ORDER BY id`,
		folderID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list files: %w", err)
	}
	defer rows.Close()

	var out []store.File
	for rows.Next() {
		var f store.File
		if err := rows.Scan(&f.ID, &f.FolderID, &f.MessageID, &f.Name, &f.Size, &f.MIME, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan file: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: rows: %w", err)
	}
	return out, nil
}

// DeleteFile removes a file by ID or returns store.ErrNotFound.
func (s *Store) DeleteFile(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM files WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: delete file: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// Close releases the pool.
func (s *Store) Close() error {
	s.pool.Close()
	return nil
}
