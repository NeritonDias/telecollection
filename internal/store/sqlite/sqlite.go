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

	_ "modernc.org/sqlite" // driver "sqlite"
)

// Store is the SQLite-backed store.Store implementation.
type Store struct {
	db *sql.DB
}

var _ store.Store = (*Store)(nil)

// NOTE: this inline schema bootstraps the store for the foundation phase.
// Subfase 0.7 introduces goose migrations that supersede it as the source of truth.
const schema = `
CREATE TABLE IF NOT EXISTS folders (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	tg_account_id INTEGER NOT NULL,
	channel_id    INTEGER NOT NULL,
	name          TEXT    NOT NULL,
	created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);`

// Open opens (and initializes) a SQLite store at the given DSN.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	// A single connection sidesteps WAL writer contention for this local workload.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Ping verifies connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// CreateFolder inserts a folder and returns it fully populated.
func (s *Store) CreateFolder(ctx context.Context, f store.Folder) (store.Folder, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO folders (tg_account_id, channel_id, name) VALUES (?, ?, ?)`,
		f.TGAccountID, f.ChannelID, f.Name)
	if err != nil {
		return store.Folder{}, fmt.Errorf("sqlite: insert folder: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return store.Folder{}, fmt.Errorf("sqlite: last insert id: %w", err)
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

// Close releases the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func parseTime(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return time.Time{}
	}
	return t
}
