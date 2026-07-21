// Package drive orchestrates folder and file operations over a connected Telegram
// client, backed by the metadata index. The HTTP layer depends only on Service,
// so endpoints and the concrete orchestration can evolve independently.
package drive

import (
	"context"
	"io"

	"github.com/gotd/td/tg"

	"github.com/telecollection/telecollection/internal/store"
	"github.com/telecollection/telecollection/internal/telegram/dialogs"
	"github.com/telecollection/telecollection/internal/transfer"
)

// Conn runs an operation with a connected *tg.Client. The server daemon (phase 8)
// keeps a persistent client.Run alive and implements this; tests use a fake. This
// decouples drive from Telegram session lifetime management.
type Conn interface {
	WithAPI(ctx context.Context, fn func(ctx context.Context, api *tg.Client) error) error
}

// Service is the drive orchestration contract consumed by the HTTP layer.
type Service interface {
	ListFolders(ctx context.Context) ([]dialogs.Folder, error)
	CreateFolder(ctx context.Context, name string) (dialogs.Folder, error)
	RenameFolder(ctx context.Context, folder dialogs.Folder, newName string) error
	DeleteFolder(ctx context.Context, folder dialogs.Folder) error

	ListFiles(ctx context.Context, folder dialogs.Folder) ([]store.File, error)
	UploadFile(ctx context.Context, folder dialogs.Folder, name string, r io.Reader, size int64, mime string, onProgress func(transfer.Progress)) (store.File, error)
	DownloadFile(ctx context.Context, folder dialogs.Folder, msgID int, w io.Writer, onProgress func(transfer.Progress)) error
	RenameFile(ctx context.Context, folder dialogs.Folder, msgID int, newName string) error
	DeleteFile(ctx context.Context, folder dialogs.Folder, msgID int) error
	MoveFile(ctx context.Context, src dialogs.Folder, msgID int, dst dialogs.Folder) (newMsgID int, err error)
}
