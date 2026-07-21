package store

import "time"

// Folder is a Telegram channel used as a drive folder.
type Folder struct {
	ID          int64
	TGAccountID int64
	ChannelID   int64
	Name        string
	CreatedAt   time.Time
}

// File is a message carrying a document, exposed as a file.
//
// ChunkManifestID links a file to a ChunkManifest when the file was too large
// for a single Telegram message and was split into chunks (Phase 3). A zero
// value means the file is single-message (no manifest); it is persisted as SQL
// NULL.
type File struct {
	ID              int64
	FolderID        int64
	MessageID       int64
	Name            string
	Size            int64
	MIME            string
	ChunkManifestID int64
	CreatedAt       time.Time
}

// ChunkManifest records how a large file was split into N chunks, each chunk
// being an independent Telegram message/document. The ordered ChunkMessageIDs
// drive reassembly on download. Chunks live in Telegram; the manifest is the
// index that ties them together (see docs/plano/FASE-3.md §3.1).
type ChunkManifest struct {
	ID              int64
	TotalSize       int64
	ChunkCount      int
	ChunkMessageIDs []int64
	CreatedAt       time.Time
}
