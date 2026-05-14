-- +goose Up
-- Per-model credit cost can now be deactivated without deleting the
-- row. When isActive=false, the model is excluded from the credit
-- billing path: /v1/models hides it for billing_source=credit, and
-- tryPriorityBilling skips credit reservation. Defaults true so
-- existing rows continue to work unchanged.
ALTER TABLE ai_gateway.model_credit_cost
    ADD COLUMN IF NOT EXISTS is_active boolean NOT NULL DEFAULT true;

CREATE INDEX IF NOT EXISTS model_credit_cost_is_active_idx
    ON ai_gateway.model_credit_cost (is_active);

-- +goose Down
DROP INDEX IF EXISTS ai_gateway.model_credit_cost_is_active_idx;
ALTER TABLE ai_gateway.model_credit_cost DROP COLUMN IF EXISTS is_active;
