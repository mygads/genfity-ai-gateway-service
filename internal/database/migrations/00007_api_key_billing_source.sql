-- +goose Up
--
-- Per-API-key billing source selector (PRD v3 Phase 3).
--
-- Each API key now carries an explicit billing_source that constrains
-- the billing priority chain at request time:
--
--   'auto'         → original 3-priority chain: subscription → credit → payg
--   'subscription' → only the unlimited plan; reject if model not covered
--   'credit'       → only credit_package; reject if balance/cost missing
--   'payg'         → only payg_topup USD balance
--
-- Default is 'auto' for backwards compatibility with existing keys.
ALTER TABLE ai_gateway.api_keys
    ADD COLUMN IF NOT EXISTS billing_source text NOT NULL DEFAULT 'auto';

ALTER TABLE ai_gateway.api_keys
    ADD CONSTRAINT api_keys_billing_source_check
    CHECK (billing_source IN ('auto', 'subscription', 'credit', 'payg'));

CREATE INDEX IF NOT EXISTS api_keys_user_billing_source_idx
    ON ai_gateway.api_keys (genfity_user_id, billing_source);

-- +goose Down
DROP INDEX IF EXISTS ai_gateway.api_keys_user_billing_source_idx;
ALTER TABLE ai_gateway.api_keys DROP CONSTRAINT IF EXISTS api_keys_billing_source_check;
ALTER TABLE ai_gateway.api_keys DROP COLUMN IF EXISTS billing_source;
