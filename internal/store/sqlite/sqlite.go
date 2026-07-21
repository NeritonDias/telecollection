// Package sqlite implements store.Store on modernc.org/sqlite (pure Go, cgo-free).
// Used in desktop mode. WAL + busy_timeout are set by the caller via the DSN.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
		`INSERT INTO files (folder_id, message_id, name, size, mime, chunk_manifest_id)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(folder_id, message_id) DO UPDATE SET
		   name = excluded.name,
		   size = excluded.size,
		   mime = excluded.mime,
		   chunk_manifest_id = excluded.chunk_manifest_id`,
		f.FolderID, f.MessageID, f.Name, f.Size, f.MIME, nullableID(f.ChunkManifestID))
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
		chunkID sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, folder_id, message_id, name, size, mime, chunk_manifest_id, created_at FROM files WHERE id = ?`, id).
		Scan(&f.ID, &f.FolderID, &f.MessageID, &f.Name, &f.Size, &f.MIME, &chunkID, &created)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return store.File{}, store.ErrNotFound
	case err != nil:
		return store.File{}, fmt.Errorf("sqlite: get file: %w", err)
	}
	f.ChunkManifestID = chunkID.Int64
	f.CreatedAt = parseTime(created)
	return f, nil
}

// ListFiles returns files for a folder, ordered by ID.
func (s *Store) ListFiles(ctx context.Context, folderID int64) ([]store.File, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, folder_id, message_id, name, size, mime, chunk_manifest_id, created_at FROM files WHERE folder_id = ? ORDER BY id`,
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
			chunkID sql.NullInt64
		)
		if err := rows.Scan(&f.ID, &f.FolderID, &f.MessageID, &f.Name, &f.Size, &f.MIME, &chunkID, &created); err != nil {
			return nil, fmt.Errorf("sqlite: scan file: %w", err)
		}
		f.ChunkManifestID = chunkID.Int64
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

// CreateManifest inserts a chunk manifest and returns it with ID/CreatedAt
// populated. ChunkMessageIDs is serialized as a JSON array of int64.
func (s *Store) CreateManifest(ctx context.Context, m store.ChunkManifest) (store.ChunkManifest, error) {
	ids, err := marshalIDs(m.ChunkMessageIDs)
	if err != nil {
		return store.ChunkManifest{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO chunk_manifests (total_size, chunk_count, chunk_message_ids)
		 VALUES (?, ?, ?)`,
		m.TotalSize, m.ChunkCount, ids)
	if err != nil {
		return store.ChunkManifest{}, fmt.Errorf("sqlite: insert manifest: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return store.ChunkManifest{}, fmt.Errorf("sqlite: manifest last insert id: %w", err)
	}
	return s.GetManifest(ctx, id)
}

// GetManifest returns the manifest by ID or store.ErrNotFound. The stored JSON
// array is deserialized back into ChunkMessageIDs.
func (s *Store) GetManifest(ctx context.Context, id int64) (store.ChunkManifest, error) {
	var (
		m       store.ChunkManifest
		ids     string
		created string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, total_size, chunk_count, chunk_message_ids, created_at FROM chunk_manifests WHERE id = ?`, id).
		Scan(&m.ID, &m.TotalSize, &m.ChunkCount, &ids, &created)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return store.ChunkManifest{}, store.ErrNotFound
	case err != nil:
		return store.ChunkManifest{}, fmt.Errorf("sqlite: get manifest: %w", err)
	}
	if m.ChunkMessageIDs, err = unmarshalIDs(ids); err != nil {
		return store.ChunkManifest{}, err
	}
	m.CreatedAt = parseTime(created)
	return m, nil
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

// nullableID maps a zero id to SQL NULL and any other value to itself, so a
// file without a manifest stores NULL in chunk_manifest_id.
func nullableID(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: id != 0}
}

// marshalIDs serializes chunk message ids as a JSON array of int64. A nil slice
// is stored as an empty array so the column is never NULL.
func marshalIDs(ids []int64) (string, error) {
	if ids == nil {
		ids = []int64{}
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("sqlite: marshal chunk ids: %w", err)
	}
	return string(b), nil
}

// unmarshalIDs parses the JSON array produced by marshalIDs.
func unmarshalIDs(s string) ([]int64, error) {
	var ids []int64
	if err := json.Unmarshal([]byte(s), &ids); err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal chunk ids: %w", err)
	}
	return ids, nil
}
