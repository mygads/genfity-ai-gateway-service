-- +goose Up
-- Partial index to make the orphan quota-reservation sweeper fast.
-- quota_counters.rows with tokens_reserved > 0 and updated_at older than
-- the threshold are rare; this index lets Postgres find them without
-- scanning the whole table.
CREATE INDEX IF NOT EXISTS quota_counters_stale_reserved_idx
    ON ai_gateway.quota_counters (updated_at)
    WHERE tokens_reserved > 0;

-- +goose Down
DROP INDEX IF EXISTS ai_gateway.quota_counters_stale_reserved_idx;
