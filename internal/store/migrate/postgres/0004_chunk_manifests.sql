-- +goose Up
CREATE TABLE IF NOT EXISTS chunk_manifests (
	id                BIGSERIAL   PRIMARY KEY,
	total_size        BIGINT      NOT NULL,
	chunk_count       INTEGER     NOT NULL,
	chunk_message_ids TEXT        NOT NULL,
	created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE files ADD COLUMN chunk_manifest_id BIGINT;

-- +goose Down
ALTER TABLE files DROP COLUMN chunk_manifest_id;

DROP TABLE chunk_manifests;
