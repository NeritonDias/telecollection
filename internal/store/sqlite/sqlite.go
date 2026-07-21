// Package sqlite implements store.Store on modernc.org/sqlite (pure Go, cgo-free).
// Used in desktop mode. WAL + busy_timeout are set by the caller via the DSN.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/telecollection/telecollection/internal/store"
	"github.com/telecollection/telecollection/internal/store/migrate"

	_ "modernc.org/sqlite" // driver "sqlite"
)

// Store is the SQLite-backed store.Store implementation.
type Store struct {
	db *sql.DB
}

var _ store.Store = (*Store)(nil)

// Open opens (and migrates) a SQLite store at the given DSN. The schema is the
// single source of truth in internal/store/migrate/sqlite (goose), applied here.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	// A single connection sidesteps WAL writer contention for this local workload.
	db.SetMaxOpenConns(1)
	if err := migrate.UpSQLite(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Ping verifies connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// CreateFolder inserts a folder or returns the existing row keyed by
// (tg_account_id, channel_id), returning it fully populated. The upsert makes
// concurrent creates for the same channel converge on a single row instead of
// duplicating. CreatedAt is preserved on the conflict path.
func (s *Store) CreateFolder(ctx context.Context, f store.Folder) (store.Folder, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO folders (tg_account_id, channel_id, name)
		 VALUES (?, ?, ?)
		 ON CONFLICT(tg_account_id, channel_id) DO UPDATE SET
		   name = excluded.name`,
		f.TGAccountID, f.ChannelID, f.Name)
	if err != nil {
		return store.Folder{}, fmt.Errorf("sqlite: upsert folder: %w", err)
	}
	// last_insert_rowid is unreliable on the DO UPDATE path, so resolve the id
	// by the unique key.
	var id int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM folders WHERE tg_account_id = ? AND channel_id = ?`, f.TGAccountID, f.ChannelID).
		Scan(&id)
	if err != nil {
		return store.Folder{}, fmt.Errorf("sqlite: resolve upserted folder id: %w", err)
	}
	return s.GetFolder(ctx, id)
}

// GetFolder returns the folder by ID or store.ErrNotFound.
func (s *Store) GetFolder(ctx context.Context, id int64) (store.Folder, error) {
	var (
		f       store.Folder
		created string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, tg_account_id, channel_id, name, created_at FROM folders WHERE id = ?`, id).
		Scan(&f.ID, &f.TGAccountID, &f.ChannelID, &f.Name, &created)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return store.Folder{}, store.ErrNotFound
	case err != nil:
		return store.Folder{}, fmt.Errorf("sqlite: get folder: %w", err)
	}
	f.CreatedAt = parseTime(created)
	return f, nil
}

// ListFolders returns folders for a Telegram account, ordered by ID.
func (s *Store) ListFolders(ctx context.Context, tgAccountID int64) ([]store.Folder, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, tg_account_id, channel_id, name, created_at FROM folders WHERE tg_account_id = ? ORDER BY id`,
		tgAccountID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list folders: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.Folder
	for rows.Next() {
		var (
			f       store.Folder
			created string
		)
		if err := rows.Scan(&f.ID, &f.TGAccountID, &f.ChannelID, &f.Name, &created); err != nil {
			return nil, fmt.Errorf("sqlite: scan folder: %w", err)
		}
		f.CreatedAt = parseTime(created)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: rows: %w", err)
	}
	return out, nil
}

// UpsertFile inserts a file or updates the existing row keyed by
// (folder_id, message_id), returning it fully populated. CreatedAt is preserved
// on update.
func (s *Store) UpsertFile(ctx context.Context, f store.File) (store.File, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO files (folder_id, message_id, name, size, mime)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(folder_id, message_id) DO UPDATE SET
		   name = excluded.name,
		   size = excluded.size,
		   mime = excluded.mime`,
		f.FolderID, f.MessageID, f.Name, f.Size, f.MIME)
	if err != nil {
		return store.File{}, fmt.Errorf("sqlite: upsert file: %w", err)
	}
	// last_insert_rowid is unreliable on the DO UPDATE path, so resolve the id
	// by the unique key.
	var id int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM files WHERE folder_id = ? AND message_id = ?`, f.FolderID, f.MessageID).
		Scan(&id)
	if err != nil {
		return store.File{}, fmt.Errorf("sqlite: resolve upserted file id: %w", err)
	}
	return s.GetFile(ctx, id)
}

// GetFile returns the file by ID or store.ErrNotFound.
func (s *Store) GetFile(ctx context.Context, id int64) (store.File, error) {
	var (
		f       store.File
		created string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, folder_id, message_id, name, size, mime, created_at FROM files WHERE id = ?`, id).
		Scan(&f.ID, &f.FolderID, &f.MessageID, &f.Name, &f.Size, &f.MIME, &created)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return store.File{}, store.ErrNotFound
	case err != nil:
		return store.File{}, fmt.Errorf("sqlite: get file: %w", err)
	}
	f.CreatedAt = parseTime(created)
	return f, nil
}

// ListFiles returns files for a folder, ordered by ID.
func (s *Store) ListFiles(ctx context.Context, folderID int64) ([]store.File, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, folder_id, message_id, name, size, mime, created_at FROM files WHERE folder_id = ? ORDER BY id`,
		folderID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list files: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.File
	for rows.Next() {
		var (
			f       store.File
			created string
		)
		if err := rows.Scan(&f.ID, &f.FolderID, &f.MessageID, &f.Name, &f.Size, &f.MIME, &created); err != nil {
			return nil, fmt.Errorf("sqlite: scan file: %w", err)
		}
		f.CreatedAt = parseTime(created)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: rows: %w", err)
	}
	return out, nil
}

// DeleteFile removes a file by ID or returns store.ErrNotFound.
func (s *Store) DeleteFile(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: delete file: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: rows affected: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// Close releases the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func parseTime(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return time.Time{}
	}
	return t
}
