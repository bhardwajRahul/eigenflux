-- +goose Up
ALTER TABLE install_tokens
    ADD COLUMN IF NOT EXISTS bilibili_track_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE install_tokens
    DROP COLUMN IF EXISTS bilibili_track_id;
