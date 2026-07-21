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

	// Close releases underlying resources.
	Close() error
}
