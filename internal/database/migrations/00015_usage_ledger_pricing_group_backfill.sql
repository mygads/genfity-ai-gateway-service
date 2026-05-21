-- +goose Up
-- 00015_usage_ledger_pricing_group_backfill.sql
--
-- Older usage_ledger rows wrote metadata->>'pricingGroup' (camelCase)
-- but the admin dashboard summarizes by metadata->>'pricing_group'
-- (snake_case). The result was a large "Unknown" bucket on the
-- AI Gateway Usage admin page even when billing_mode was perfectly
-- valid. This migration backfills metadata->>'pricing_group' from
-- the row's billing_mode so historical rows get classified
-- correctly. Going forward the writer derives pricing_group from
-- the reservation's billing_mode, so no future rows should land in
-- "unknown" unless billing_mode itself is empty.

-- +goose StatementBegin
-- Map billing_mode -> pricing_group on rows that don't already have one.
UPDATE ai_gateway.usage_ledger
SET metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object(
    'pricing_group',
    CASE billing_mode
        WHEN 'unlimited' THEN 'unlimited_plan'
        WHEN 'credit_package' THEN 'credit_package'
        WHEN 'payg_topup' THEN 'payg_topup'
        ELSE NULL
    END
)
WHERE billing_mode IS NOT NULL
  AND billing_mode IN ('unlimited', 'credit_package', 'payg_topup')
  AND COALESCE(NULLIF(metadata->>'pricing_group', ''), '') = '';
-- +goose StatementEnd

-- +goose StatementBegin
-- Promote camelCase pricingGroup (legacy) into snake_case for any row
-- where snake_case is still missing.
UPDATE ai_gateway.usage_ledger
SET metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object(
    'pricing_group',
    metadata->>'pricingGroup'
)
WHERE COALESCE(NULLIF(metadata->>'pricing_group', ''), '') = ''
  AND COALESCE(NULLIF(metadata->>'pricingGroup', ''), '') <> '';
-- +goose StatementEnd

-- +goose Down
-- Backfill is non-destructive; no down migration.
SELECT 1;
