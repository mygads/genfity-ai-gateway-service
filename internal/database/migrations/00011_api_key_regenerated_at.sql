-- +goose Up
ALTER TABLE ai_gateway.api_keys
    ADD COLUMN IF NOT EXISTS regenerated_at timestamp with time zone;

-- +goose Down
ALTER TABLE ai_gateway.api_keys DROP COLUMN IF EXISTS regenerated_at;
