-- +goose Up
--
-- Drop the legacy 'auto' billing_source value (PRD v3 Phase 3 cleanup).
--
-- 'auto' used to mean "cascade through subscription → credit → payg in
-- priority order". The cascade was a leaky abstraction: a user with
-- both an unlimited trial and a credit_package would see their
-- credit-billed requests for paid models tick the unlimited plan's
-- MaxRequestsPerPeriod counter, banning them well below their real
-- cap. After 2026-05 the gateway pins each key to exactly one billing
-- source. The genfity-app UI no longer offers 'auto' as an option.
--
-- This migration:
--   1. Migrates any remaining 'auto' rows to the safest equivalent.
--      Users with an active unlimited plan get 'subscription'; everyone
--      else falls to 'credit' (the existing keys overwhelmingly belong
--      to credit-package users — see audit on 2026-05-21).
--   2. Tightens the CHECK constraint to forbid 'auto'.
--   3. Updates the column default so future inserts that don't specify
--      a source land on 'subscription'.
--
-- This is a one-way migration. Down restores the old constraint but
-- cannot recover which rows were originally 'auto'; that information
-- is irrelevant going forward because the runtime no longer treats
-- 'auto' specially.
-- +goose StatementBegin
UPDATE ai_gateway.api_keys ak
SET billing_source = 'subscription'
WHERE ak.billing_source = 'auto'
  AND EXISTS (
    SELECT 1 FROM ai_gateway.customer_entitlements ce
    WHERE ce.genfity_user_id = ak.genfity_user_id
      AND ce.status = 'active'
      AND COALESCE(ce.pricing_group, ce.metadata->>'pricingGroup') IN ('unlimited', 'unlimited_plan')
  );
-- +goose StatementEnd

UPDATE ai_gateway.api_keys
SET billing_source = 'credit'
WHERE billing_source = 'auto';

ALTER TABLE ai_gateway.api_keys DROP CONSTRAINT IF EXISTS api_keys_billing_source_check;
ALTER TABLE ai_gateway.api_keys
    ADD CONSTRAINT api_keys_billing_source_check
    CHECK (billing_source IN ('subscription', 'credit', 'payg'));

ALTER TABLE ai_gateway.api_keys
    ALTER COLUMN billing_source SET DEFAULT 'subscription';

-- +goose Down
ALTER TABLE ai_gateway.api_keys
    ALTER COLUMN billing_source SET DEFAULT 'auto';

ALTER TABLE ai_gateway.api_keys DROP CONSTRAINT IF EXISTS api_keys_billing_source_check;
ALTER TABLE ai_gateway.api_keys
    ADD CONSTRAINT api_keys_billing_source_check
    CHECK (billing_source IN ('auto', 'subscription', 'credit', 'payg'));
