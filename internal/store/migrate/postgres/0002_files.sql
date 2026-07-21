-- +goose Up
CREATE TABLE IF NOT EXISTS files (
	id         BIGSERIAL   PRIMARY KEY,
	folder_id  BIGINT      NOT NULL,
	message_id BIGINT      NOT NULL,
	name       TEXT        NOT NULL,
	size       BIGINT      NOT NULL,
	mime       TEXT        NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (folder_id, message_id)
);

-- +goose Down
DROP TABLE files;
