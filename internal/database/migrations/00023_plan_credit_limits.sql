-- +goose Up
ALTER TABLE ai_gateway.subscription_plan_snapshots
    ADD COLUMN IF NOT EXISTS credit_limit_per_day numeric(18,4),
    ADD COLUMN IF NOT EXISTS credit_limit_per_period numeric(18,4);

-- +goose Down
ALTER TABLE ai_gateway.subscription_plan_snapshots
    DROP COLUMN IF EXISTS credit_limit_per_period,
    DROP COLUMN IF EXISTS credit_limit_per_day;
