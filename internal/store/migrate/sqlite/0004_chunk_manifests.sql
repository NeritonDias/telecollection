-- +goose Up
CREATE TABLE IF NOT EXISTS chunk_manifests (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	total_size        INTEGER NOT NULL,
	chunk_count       INTEGER NOT NULL,
	chunk_message_ids TEXT    NOT NULL,
	created_at        TEXT    NOT NULL DEFAULT (datetime('now'))
);

ALTER TABLE files ADD COLUMN chunk_manifest_id INTEGER;

-- +goose Down
ALTER TABLE files DROP COLUMN chunk_manifest_id;

DROP TABLE chunk_manifests;
