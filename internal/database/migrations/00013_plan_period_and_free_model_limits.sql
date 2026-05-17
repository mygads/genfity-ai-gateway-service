-- +goose Up
-- Plan: cap total requests per subscription period. NULL/0 = unlimited.
ALTER TABLE ai_gateway.subscription_plan_snapshots
    ADD COLUMN IF NOT EXISTS max_requests_per_period integer;

-- Model: free-tier flag + per-(user,model) limits enforced when is_free=true.
ALTER TABLE ai_gateway.ai_models
    ADD COLUMN IF NOT EXISTS is_free boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS free_limit_rpd integer,
    ADD COLUMN IF NOT EXISTS free_limit_rpm integer,
    ADD COLUMN IF NOT EXISTS free_limit_tpd bigint;

CREATE INDEX IF NOT EXISTS ai_models_is_free_idx ON ai_gateway.ai_models (is_free);

-- +goose Down
DROP INDEX IF EXISTS ai_gateway.ai_models_is_free_idx;
ALTER TABLE ai_gateway.ai_models
    DROP COLUMN IF EXISTS free_limit_tpd,
    DROP COLUMN IF EXISTS free_limit_rpm,
    DROP COLUMN IF EXISTS free_limit_rpd,
    DROP COLUMN IF EXISTS is_free;
ALTER TABLE ai_gateway.subscription_plan_snapshots
    DROP COLUMN IF EXISTS max_requests_per_period;
