-- +goose Up
--
-- PRD v3 Phase 1B: add credit + PAYG balance columns to customer_entitlements
-- so the gateway can enforce the 3-priority billing chain (unlimited →
-- credit → PAYG) without a round trip to genfity-app on every request.
--
-- Source of truth for balances is genfity-app's AiGatewayCreditLedger
-- table. These columns are synced in on every entitlement sync and
-- mutated by the gateway on each reserve/finalize cycle. A periodic
-- reconciliation job in genfity-app verifies them against the ledger.
--
-- `credit_balance`          — unspent credits from credit_topup entries,
--                              net of credit_debit + credit_expire.
-- `credit_balance_reserved` — reservations held for in-flight requests.
-- `credit_expires_at`       — earliest expiry among unspent credit batches,
--                              denormalized so the hot path can skip a join.
-- `payg_usd_balance`        — unspent PAYG dollars, net of debits.
-- `payg_usd_balance_reserved` — reservations held for in-flight requests.
-- `pricing_group`           — "unlimited" | "credit_package" | "payg_topup".
--                              Duplicates genfity-app for local routing.
ALTER TABLE ai_gateway.customer_entitlements
    ADD COLUMN IF NOT EXISTS credit_balance numeric(18, 4) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS credit_balance_reserved numeric(18, 4) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS credit_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS payg_usd_balance numeric(18, 6) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS payg_usd_balance_reserved numeric(18, 6) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS pricing_group text;

-- Non-negative invariants. Negative balances would indicate a logic bug;
-- the CHECKs surface the bug at the storage layer instead of silently
-- corrupting a user's account.
ALTER TABLE ai_gateway.customer_entitlements
    ADD CONSTRAINT customer_entitlements_credit_balance_nonneg
        CHECK (credit_balance >= 0);
ALTER TABLE ai_gateway.customer_entitlements
    ADD CONSTRAINT customer_entitlements_credit_reserved_nonneg
        CHECK (credit_balance_reserved >= 0);
ALTER TABLE ai_gateway.customer_entitlements
    ADD CONSTRAINT customer_entitlements_payg_balance_nonneg
        CHECK (payg_usd_balance >= 0);
ALTER TABLE ai_gateway.customer_entitlements
    ADD CONSTRAINT customer_entitlements_payg_reserved_nonneg
        CHECK (payg_usd_balance_reserved >= 0);

-- Partial index for the credit-expiry sweep cron — only rows with a
-- pending expiry need to be scanned, so the WHERE clause keeps the
-- index tiny on a large entitlements table.
CREATE INDEX IF NOT EXISTS customer_entitlements_credit_expires_idx
    ON ai_gateway.customer_entitlements (credit_expires_at)
    WHERE credit_expires_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS customer_entitlements_pricing_group_idx
    ON ai_gateway.customer_entitlements (pricing_group, status);

-- Exclusivity: a user can hold at most one active unlimited
-- entitlement. Credit-package and payg_topup entitlements are
-- stackable, so the partial unique index is scoped to
-- pricing_group='unlimited' and status='active' only.
CREATE UNIQUE INDEX IF NOT EXISTS customer_entitlements_unlimited_unique
    ON ai_gateway.customer_entitlements (genfity_user_id)
    WHERE pricing_group = 'unlimited' AND status = 'active';

-- +goose Down
DROP INDEX IF EXISTS ai_gateway.customer_entitlements_unlimited_unique;
DROP INDEX IF EXISTS ai_gateway.customer_entitlements_pricing_group_idx;
DROP INDEX IF EXISTS ai_gateway.customer_entitlements_credit_expires_idx;

ALTER TABLE ai_gateway.customer_entitlements
    DROP CONSTRAINT IF EXISTS customer_entitlements_credit_balance_nonneg,
    DROP CONSTRAINT IF EXISTS customer_entitlements_credit_reserved_nonneg,
    DROP CONSTRAINT IF EXISTS customer_entitlements_payg_balance_nonneg,
    DROP CONSTRAINT IF EXISTS customer_entitlements_payg_reserved_nonneg;

ALTER TABLE ai_gateway.customer_entitlements
    DROP COLUMN IF EXISTS pricing_group,
    DROP COLUMN IF EXISTS payg_usd_balance_reserved,
    DROP COLUMN IF EXISTS payg_usd_balance,
    DROP COLUMN IF EXISTS credit_expires_at,
    DROP COLUMN IF EXISTS credit_balance_reserved,
    DROP COLUMN IF EXISTS credit_balance;
