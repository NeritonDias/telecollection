// Package store defines the persistence contract for TeleCollection and the
// domain types it operates on. Implementations live in sibling packages
// (store/sqlite, store/postgres) and are validated against storetest.Contract.
//
// The Telegram account is the source of truth for data; a Store is an
// acceleration cache/index and is disposable (see docs/ARQUITETURA.md §6.4).
package store

import (
	"context"
	"errors"
)

// Sentinel errors returned by every implementation.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// Store is the persistence interface. It grows in later phases; this is the
// foundation subset. All implementations must satisfy storetest.Contract.
type Store interface {
	// Ping verifies connectivity/readiness.
	Ping(ctx context.Context) error

	// CreateFolder inserts a folder and returns it with ID/CreatedAt populated.
	CreateFolder(ctx context.Context, f Folder) (Folder, error)
	// GetFolder returns the folder by ID, or ErrNotFound.
	GetFolder(ctx context.Context, id int64) (Folder, error)
	// ListFolders returns folders for a Telegram account.
	ListFolders(ctx context.Context, tgAccountID int64) ([]Folder, error)

	// UpsertFile inserts a file or updates the existing one keyed by
	// (folder_id, message_id), returning it with ID/CreatedAt populated.
	UpsertFile(ctx context.Context, f File) (File, error)
	// GetFile returns the file by ID, or ErrNotFound.
	GetFile(ctx context.Context, id int64) (File, error)
	// ListFiles returns files for a folder, ordered by ID.
	ListFiles(ctx context.Context, folderID int64) ([]File, error)
	// DeleteFile removes a file by ID, or returns ErrNotFound.
	DeleteFile(ctx context.Context, id int64) error

	// Close releases underlying resources.
	Close() error
}
