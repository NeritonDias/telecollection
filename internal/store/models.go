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
type File struct {
	ID        int64
	FolderID  int64
	MessageID int64
	Name      string
	Size      int64
	MIME      string
	CreatedAt time.Time
}
