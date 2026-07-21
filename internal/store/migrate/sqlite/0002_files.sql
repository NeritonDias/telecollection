-- +goose Up
CREATE TABLE IF NOT EXISTS files (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	folder_id  INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	name       TEXT    NOT NULL,
	size       INTEGER NOT NULL,
	mime       TEXT    NOT NULL,
	created_at TEXT    NOT NULL DEFAULT (datetime('now')),
	UNIQUE (folder_id, message_id)
);

-- +goose Down
DROP TABLE files;
