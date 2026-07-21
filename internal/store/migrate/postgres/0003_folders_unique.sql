-- +goose Up
CREATE UNIQUE INDEX IF NOT EXISTS ux_folders_account_channel ON folders(tg_account_id, channel_id);

-- +goose Down
DROP INDEX IF EXISTS ux_folders_account_channel;
