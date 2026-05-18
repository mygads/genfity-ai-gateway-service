-- +goose Up
-- Plan: cap requests per calendar day (UTC) per user on the plan.
-- Independent from max_requests_per_period — admin may set either, both,
-- or neither. NULL/0 = no daily limit. Plan-level RPD is separate from
-- the per-(user, model) free-tier RPD on ai_models.free_limit_rpd.
ALTER TABLE ai_gateway.subscription_plan_snapshots
    ADD COLUMN IF NOT EXISTS rate_limit_rpd integer;

-- +goose Down
ALTER TABLE ai_gateway.subscription_plan_snapshots
    DROP COLUMN IF EXISTS rate_limit_rpd;
