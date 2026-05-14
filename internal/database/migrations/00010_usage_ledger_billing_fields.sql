-- +goose Up
-- Add billing-mode and remaining-balance fields to usage_ledger so the
-- customer-facing usage page can render per-row credit charges, USD
-- charges, and remaining balance after each request without joining
-- against the ledger every time.
--
-- billing_mode mirrors runtimeReservation.BillingMode emitted by
-- gateway_handler.tryPriorityBilling: "subscription" | "credit_package" |
-- "payg_topup". For requests that didn't go through any priority
-- billing path (e.g. unlimited subscription) it is null.
ALTER TABLE ai_gateway.usage_ledger
    ADD COLUMN IF NOT EXISTS billing_mode TEXT,
    ADD COLUMN IF NOT EXISTS amount_credits NUMERIC(18,4),
    ADD COLUMN IF NOT EXISTS balance_after_credits NUMERIC(18,4),
    ADD COLUMN IF NOT EXISTS balance_after_usd NUMERIC(18,6);

CREATE INDEX IF NOT EXISTS usage_ledger_billing_mode_idx
    ON ai_gateway.usage_ledger (billing_mode);

-- +goose Down
DROP INDEX IF EXISTS ai_gateway.usage_ledger_billing_mode_idx;
ALTER TABLE ai_gateway.usage_ledger
    DROP COLUMN IF EXISTS balance_after_usd,
    DROP COLUMN IF EXISTS balance_after_credits,
    DROP COLUMN IF EXISTS amount_credits,
    DROP COLUMN IF EXISTS billing_mode;
