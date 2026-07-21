-- +goose Up
CREATE TABLE IF NOT EXISTS folders (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	tg_account_id INTEGER NOT NULL,
	channel_id    INTEGER NOT NULL,
	name          TEXT    NOT NULL,
	created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- +goose Down
DROP TABLE folders;
