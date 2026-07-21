// Package index is a thin metadata cache over store.Store, used by the transfer
// phases to accelerate folder/file listing and lookup. The Telegram account
// remains the source of truth (see docs/ARQUITETURA.md §6.4); the index is a
// disposable acceleration layer.
package index

import (
	"context"
	"fmt"

	"github.com/telecollection/telecollection/internal/store"
)

// Index caches folder/file metadata on a store.Store.
type Index struct {
	store store.Store
}

// New returns an Index backed by s.
func New(s store.Store) *Index {
	return &Index{store: s}
}

// UpsertFolder records folder metadata in the cache and returns it with
// ID/CreatedAt populated.
func (i *Index) UpsertFolder(ctx context.Context, f store.Folder) (store.Folder, error) {
	return i.store.CreateFolder(ctx, f)
}

// ListFolders returns cached folders for a Telegram account.
func (i *Index) ListFolders(ctx context.Context, tgAccountID int64) ([]store.Folder, error) {
	return i.store.ListFolders(ctx, tgAccountID)
}

// UpsertFile records file metadata, keyed by (folder_id, message_id), and
// returns it with ID/CreatedAt populated.
func (i *Index) UpsertFile(ctx context.Context, f store.File) (store.File, error) {
	return i.store.UpsertFile(ctx, f)
}

// ListFiles returns cached files for a folder.
func (i *Index) ListFiles(ctx context.Context, folderID int64) ([]store.File, error) {
	return i.store.ListFiles(ctx, folderID)
}

// DeleteFile prunes the cached row for (folderID, msgID) so a subsequent
// ListFiles no longer returns a file that has been deleted or moved on
// Telegram. It resolves the row by scanning ListFiles(folderID) for a matching
// MessageID, then deletes it by primary key.
//
// A message that is not cached is a no-op success (idempotent): the index is a
// disposable acceleration layer, not an authority, so pruning something already
// absent is not an error.
func (i *Index) DeleteFile(ctx context.Context, folderID, msgID int64) error {
	files, err := i.store.ListFiles(ctx, folderID)
	if err != nil {
		return fmt.Errorf("index: listing files to delete: %w", err)
	}
	for _, f := range files {
		if f.MessageID == msgID {
			if err := i.store.DeleteFile(ctx, f.ID); err != nil {
				return fmt.Errorf("index: deleting file: %w", err)
			}
			return nil
		}
	}
	return nil
}
