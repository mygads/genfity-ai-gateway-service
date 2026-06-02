-- +goose Up
-- Store cache-read and cache-creation input tokens separately so prompt
-- caching can be billed and audited accurately.
ALTER TABLE ai_gateway.usage_ledger
    ADD COLUMN IF NOT EXISTS cache_read_input_tokens bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_creation_input_tokens bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ai_gateway.usage_ledger
    DROP COLUMN IF EXISTS cache_read_input_tokens,
    DROP COLUMN IF EXISTS cache_creation_input_tokens;
