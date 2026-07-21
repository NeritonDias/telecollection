package drive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/index"
	"github.com/telecollection/telecollection/internal/store"
	"github.com/telecollection/telecollection/internal/telegram/dialogs"
	"github.com/telecollection/telecollection/internal/telegram/download"
	"github.com/telecollection/telecollection/internal/telegram/fileops"
	"github.com/telecollection/telecollection/internal/telegram/folders"
	"github.com/telecollection/telecollection/internal/telegram/upload"
	"github.com/telecollection/telecollection/internal/transfer"
)

// indexAccountID is the Telegram-account namespace the metadata index is keyed
// under in Fase 2. The Conn abstraction (and therefore New) carries no account
// identity, so the index collapses to a single namespace here; deriving the real
// self-id from Telegram and supporting multiple accounts is deferred to a later
// phase. The store.Folder.TGAccountID column already exists for that growth.
const indexAccountID int64 = 0

// driveService is the concrete Service. It orchestrates the stateless Telegram
// operation packages (dialogs, folders, upload, download, fileops) inside a
// connected client supplied by conn, and keeps the metadata index (idx) in sync
// as an acceleration cache. The Telegram account remains the source of truth;
// the index is disposable (see docs/ARQUITETURA.md §6.4).
type driveService struct {
	conn Conn
	idx  *index.Index
}

// New returns a Service that runs every Telegram operation through conn and
// records metadata in idx.
func New(conn Conn, idx *index.Index) Service {
	return &driveService{conn: conn, idx: idx}
}

// ListFolders returns the account's TeleCollection folders straight from
// Telegram (the source of truth), via dialogs.List.
func (s *driveService) ListFolders(ctx context.Context) ([]dialogs.Folder, error) {
	var out []dialogs.Folder
	err := s.conn.WithAPI(ctx, func(ctx context.Context, api *tg.Client) error {
		fs, err := dialogs.List(ctx, api)
		if err != nil {
			return err
		}
		out = fs
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("drive: list folders: %w", err)
	}
	return out, nil
}

// CreateFolder creates a marked broadcast channel via folders.Create and records
// it in the index. The channel is the source of truth: if indexing fails the
// created folder is still returned alongside the error so the caller can address
// the live channel.
func (s *driveService) CreateFolder(ctx context.Context, name string) (dialogs.Folder, error) {
	if strings.TrimSpace(name) == "" {
		return dialogs.Folder{}, errors.New("drive: folder name is required")
	}
	var created dialogs.Folder
	err := s.conn.WithAPI(ctx, func(ctx context.Context, api *tg.Client) error {
		f, err := folders.Create(ctx, api, name)
		if err != nil {
			return err
		}
		created = f
		return nil
	})
	if err != nil {
		return dialogs.Folder{}, fmt.Errorf("drive: create folder: %w", err)
	}
	if _, err := s.ensureFolder(ctx, created); err != nil {
		return created, fmt.Errorf("drive: indexing new folder: %w", err)
	}
	return created, nil
}

// RenameFolder edits the folder channel's title via folders.Rename. The index
// folder name is cosmetic and is left to be refreshed by the next ListFolders
// reconcile against Telegram.
func (s *driveService) RenameFolder(ctx context.Context, folder dialogs.Folder, newName string) error {
	if strings.TrimSpace(newName) == "" {
		return errors.New("drive: new folder name is required")
	}
	err := s.conn.WithAPI(ctx, func(ctx context.Context, api *tg.Client) error {
		return folders.Rename(ctx, api, folder, newName)
	})
	if err != nil {
		return fmt.Errorf("drive: rename folder: %w", err)
	}
	return nil
}

// DeleteFolder removes the folder channel via folders.Delete. The index exposes
// no folder delete in Fase 2, so any cached folder row is left as a stale entry
// (the index is disposable); Telegram is the source of truth.
func (s *driveService) DeleteFolder(ctx context.Context, folder dialogs.Folder) error {
	err := s.conn.WithAPI(ctx, func(ctx context.Context, api *tg.Client) error {
		return folders.Delete(ctx, api, folder)
	})
	if err != nil {
		return fmt.Errorf("drive: delete folder: %w", err)
	}
	return nil
}

// ListFiles returns a folder's files from the metadata index.
//
// Fase 2 strategy: the index is a cache populated by UploadFile, and ListFiles
// serves it directly (no network). This is the accepted Fase-2 behaviour; a
// Telegram-side history scan that reconciles the index with the source of truth
// (adding files uploaded out-of-band, dropping deleted/moved ones) is a later
// task. The folder's numeric index id is resolved from its ChannelID via
// ensureFolder.
func (s *driveService) ListFiles(ctx context.Context, folder dialogs.Folder) ([]store.File, error) {
	f, err := s.ensureFolder(ctx, folder)
	if err != nil {
		return nil, err
	}
	files, err := s.idx.ListFiles(ctx, f.ID)
	if err != nil {
		return nil, fmt.Errorf("drive: listing files: %w", err)
	}
	return files, nil
}

// UploadFile streams a document into folder via upload.File, then records it in
// the index with the resolved folder id (upload.File leaves FolderID at 0 by
// design). The stored store.File is returned. If indexing fails after a
// successful upload, the uploaded metadata is returned with the error, since the
// file already lives on Telegram.
func (s *driveService) UploadFile(ctx context.Context, folder dialogs.Folder, name string, r io.Reader, size int64, mime string, onProgress func(transfer.Progress)) (store.File, error) {
	var uploaded store.File
	err := s.conn.WithAPI(ctx, func(ctx context.Context, api *tg.Client) error {
		f, err := upload.File(ctx, api, folder, name, r, size, mime, onProgress)
		if err != nil {
			return err
		}
		uploaded = f
		return nil
	})
	if err != nil {
		return store.File{}, fmt.Errorf("drive: upload file: %w", err)
	}
	f, err := s.ensureFolder(ctx, folder)
	if err != nil {
		return uploaded, fmt.Errorf("drive: resolving folder for index: %w", err)
	}
	uploaded.FolderID = f.ID
	saved, err := s.idx.UpsertFile(ctx, uploaded)
	if err != nil {
		return uploaded, fmt.Errorf("drive: indexing uploaded file: %w", err)
	}
	return saved, nil
}

// DownloadFile streams the document carried by msgID in folder into w via
// download.File, which verifies the written size against the document size.
func (s *driveService) DownloadFile(ctx context.Context, folder dialogs.Folder, msgID int, w io.Writer, onProgress func(transfer.Progress)) error {
	err := s.conn.WithAPI(ctx, func(ctx context.Context, api *tg.Client) error {
		return download.File(ctx, api, folder, msgID, w, onProgress)
	})
	if err != nil {
		return fmt.Errorf("drive: download file: %w", err)
	}
	return nil
}

// RenameFile edits a file's caption via fileops.Rename and mirrors the new name
// into the index cache.
func (s *driveService) RenameFile(ctx context.Context, folder dialogs.Folder, msgID int, newName string) error {
	err := s.conn.WithAPI(ctx, func(ctx context.Context, api *tg.Client) error {
		return fileops.Rename(ctx, api, folder, msgID, newName)
	})
	if err != nil {
		return fmt.Errorf("drive: rename file: %w", err)
	}
	if err := s.reindexRenamedFile(ctx, folder, msgID, newName); err != nil {
		return err
	}
	return nil
}

// DeleteFile removes a file's message via fileops.Delete and prunes the cached
// row from the index, so a subsequent ListFiles no longer serves the deleted
// file. Pruning happens only after the network delete succeeds; a message that
// is not cached is a no-op (the index is disposable). Telegram remains the
// source of truth.
func (s *driveService) DeleteFile(ctx context.Context, folder dialogs.Folder, msgID int) error {
	err := s.conn.WithAPI(ctx, func(ctx context.Context, api *tg.Client) error {
		return fileops.Delete(ctx, api, folder, msgID)
	})
	if err != nil {
		return fmt.Errorf("drive: delete file: %w", err)
	}
	f, err := s.ensureFolder(ctx, folder)
	if err != nil {
		return err
	}
	if err := s.idx.DeleteFile(ctx, f.ID, int64(msgID)); err != nil {
		return fmt.Errorf("drive: pruning index after delete: %w", err)
	}
	return nil
}

// MoveFile forwards a file into dst and deletes it from src via fileops.Move,
// returning the new message id. The move is not atomic (see fileops.Move): on a
// partial failure the new id is still returned alongside the error so the caller
// can reconcile. The index is updated additively with the destination record.
func (s *driveService) MoveFile(ctx context.Context, src dialogs.Folder, msgID int, dst dialogs.Folder) (int, error) {
	var newID int
	err := s.conn.WithAPI(ctx, func(ctx context.Context, api *tg.Client) error {
		id, err := fileops.Move(ctx, api, src, msgID, dst)
		newID = id
		return err
	})
	if err != nil {
		return newID, fmt.Errorf("drive: move file: %w", err)
	}
	if err := s.reindexMovedFile(ctx, src, msgID, dst, newID); err != nil {
		return newID, err
	}
	return newID, nil
}

// ensureFolder resolves the index record for a Telegram folder, creating it on
// first use. It is a DB-backed get-or-create keyed on ChannelID within the
// indexAccountID namespace: it scans the indexed folders for a matching
// ChannelID and, failing that, records a new one. This keeps the resolution
// deterministic and duplicate-free without an in-memory cache, working around
// the absence of a lookup-by-channel method on the index in Fase 2.
func (s *driveService) ensureFolder(ctx context.Context, folder dialogs.Folder) (store.Folder, error) {
	existing, err := s.idx.ListFolders(ctx, indexAccountID)
	if err != nil {
		return store.Folder{}, fmt.Errorf("drive: reading indexed folders: %w", err)
	}
	for _, f := range existing {
		if f.ChannelID == folder.ChannelID {
			return f, nil
		}
	}
	created, err := s.idx.UpsertFolder(ctx, store.Folder{
		TGAccountID: indexAccountID,
		ChannelID:   folder.ChannelID,
		Name:        dialogs.DisplayName(folder.Title),
	})
	if err != nil {
		return store.Folder{}, fmt.Errorf("drive: recording folder in index: %w", err)
	}
	return created, nil
}

// reindexRenamedFile mirrors a rename into the index by updating the cached
// file's name. A file that is not in the cache is a no-op success: the index is
// a disposable acceleration layer, not an authority.
func (s *driveService) reindexRenamedFile(ctx context.Context, folder dialogs.Folder, msgID int, newName string) error {
	f, err := s.ensureFolder(ctx, folder)
	if err != nil {
		return err
	}
	files, err := s.idx.ListFiles(ctx, f.ID)
	if err != nil {
		return fmt.Errorf("drive: reading index for rename: %w", err)
	}
	for _, file := range files {
		if file.MessageID == int64(msgID) {
			file.Name = newName
			if _, err := s.idx.UpsertFile(ctx, file); err != nil {
				return fmt.Errorf("drive: updating index after rename: %w", err)
			}
			return nil
		}
	}
	return nil
}

// reindexMovedFile mirrors a move into the index by recording the file at its
// destination (folder+new message id), carrying the cached name/size/MIME over,
// and pruning the source cache entry so ListFiles on the origin no longer serves
// the moved file. A file that is not in the source cache is a no-op success.
func (s *driveService) reindexMovedFile(ctx context.Context, src dialogs.Folder, msgID int, dst dialogs.Folder, newMsgID int) error {
	srcFolder, err := s.ensureFolder(ctx, src)
	if err != nil {
		return err
	}
	files, err := s.idx.ListFiles(ctx, srcFolder.ID)
	if err != nil {
		return fmt.Errorf("drive: reading index for move: %w", err)
	}
	var moved store.File
	found := false
	for _, file := range files {
		if file.MessageID == int64(msgID) {
			moved = file
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	dstFolder, err := s.ensureFolder(ctx, dst)
	if err != nil {
		return err
	}
	if _, err := s.idx.UpsertFile(ctx, store.File{
		FolderID:  dstFolder.ID,
		MessageID: int64(newMsgID),
		Name:      moved.Name,
		Size:      moved.Size,
		MIME:      moved.MIME,
	}); err != nil {
		return fmt.Errorf("drive: updating index after move: %w", err)
	}
	if err := s.idx.DeleteFile(ctx, srcFolder.ID, int64(msgID)); err != nil {
		return fmt.Errorf("drive: pruning source index after move: %w", err)
	}
	return nil
}
