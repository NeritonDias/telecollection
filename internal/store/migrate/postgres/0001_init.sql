-- +goose Up
CREATE TABLE IF NOT EXISTS folders (
	id            BIGSERIAL   PRIMARY KEY,
	tg_account_id BIGINT      NOT NULL,
	channel_id    BIGINT      NOT NULL,
	name          TEXT        NOT NULL,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE folders;
