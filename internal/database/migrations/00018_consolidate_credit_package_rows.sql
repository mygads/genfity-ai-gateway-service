-- +goose Up
--
-- Consolidate multi-row credit_package entitlements into a single
-- active row per user (PRD v3 Phase 4: single-row credit billing).
--
-- Why this exists: the gateway used to allow ON CONFLICT
-- (genfity_user_id, plan_code) to upsert entitlements. genfity-app
-- updates ITS row in place when a user buys a higher-tier credit pack
-- (starter -> developer), but the gateway saw a NEW (user, plan_code)
-- pair and inserted a fresh row instead — leaving the old plan_code's
-- balance ALSO active. ReserveRequestCredits then picked whichever row
-- had the most balance (ORDER BY credit_balance DESC), which could be
-- the legacy starter row, while genfity-app's User.aiGatewayCreditBalance
-- only tracked one number. Result: drift between app UI ("$177.60")
-- and gateway runtime ("$181.90 across 2 rows"), plus non-deterministic
-- reserve/finalize across rows.
--
-- This migration:
--   1. For each user with multiple active credit_package rows, picks
--      the most-recently-updated row as the "winner".
--   2. Sums credit_balance and credit_balance_reserved from all the
--      losing rows into the winner.
--   3. Marks the loser rows status='consolidated' (NOT 'replaced' —
--      replaced is reserved for unlimited_plan upgrades; we use a
--      distinct status so future audits can tell the two apart).
--   4. Adds a partial unique index that enforces "one active
--      credit_package row per user" going forward, so the upsert
--      can use ON CONFLICT (genfity_user_id) WHERE pricing_group=...
--      from now on.
--
-- This is a one-way migration. The down step removes the unique index
-- but does NOT split consolidated balances back into per-plan rows —
-- that information is preserved in metadata.consolidatedFrom for audit
-- but is not reversible because the running balance has been merged.
-- +goose StatementBegin
DO $$
DECLARE
    user_id text;
    winner_row record;
    losers_count int;
    losers_credit numeric(18,4);
    losers_reserved numeric(18,4);
    loser_ids uuid[];
    loser_plans text[];
BEGIN
    FOR user_id IN
        SELECT genfity_user_id
        FROM ai_gateway.customer_entitlements
        WHERE status = 'active' AND pricing_group = 'credit_package'
        GROUP BY genfity_user_id
        HAVING count(*) > 1
    LOOP
        -- Winner: most recently updated active credit_package row.
        SELECT id, plan_code, credit_balance, credit_balance_reserved
        INTO winner_row
        FROM ai_gateway.customer_entitlements
        WHERE genfity_user_id = user_id
          AND status = 'active'
          AND pricing_group = 'credit_package'
        ORDER BY updated_at DESC
        LIMIT 1;

        -- Sum balances from losers (everything else for this user).
        SELECT
            count(*),
            COALESCE(sum(credit_balance), 0),
            COALESCE(sum(credit_balance_reserved), 0),
            array_agg(id),
            array_agg(plan_code)
        INTO losers_count, losers_credit, losers_reserved, loser_ids, loser_plans
        FROM ai_gateway.customer_entitlements
        WHERE genfity_user_id = user_id
          AND status = 'active'
          AND pricing_group = 'credit_package'
          AND id <> winner_row.id;

        -- Merge into winner.
        UPDATE ai_gateway.customer_entitlements
        SET credit_balance = credit_balance + losers_credit,
            credit_balance_reserved = credit_balance_reserved + losers_reserved,
            metadata = jsonb_set(
                COALESCE(metadata, '{}'::jsonb),
                '{consolidatedFrom}',
                to_jsonb(loser_plans)
            ),
            updated_at = now()
        WHERE id = winner_row.id;

        -- Mark losers consolidated.
        UPDATE ai_gateway.customer_entitlements
        SET status = 'consolidated',
            credit_balance = 0,
            credit_balance_reserved = 0,
            metadata = jsonb_set(
                COALESCE(metadata, '{}'::jsonb),
                '{consolidatedInto}',
                to_jsonb(winner_row.plan_code)
            ),
            updated_at = now()
        WHERE id = ANY(loser_ids);

        RAISE NOTICE 'consolidated user=% winner=% losers=% credit_merged=% reserved_merged=%',
            user_id, winner_row.plan_code, losers_count, losers_credit, losers_reserved;
    END LOOP;
END $$;
-- +goose StatementEnd

-- Enforce one active credit_package row per user going forward.
CREATE UNIQUE INDEX IF NOT EXISTS customer_entitlements_credit_package_unique
    ON ai_gateway.customer_entitlements (genfity_user_id)
    WHERE pricing_group = 'credit_package' AND status = 'active';

-- Mirror the same rule for payg_topup so PAYG drift can't recur via
-- multiple active rows. PRD v3 says a user holds at most one PAYG
-- balance row; this index makes that a hard constraint.
CREATE UNIQUE INDEX IF NOT EXISTS customer_entitlements_payg_topup_unique
    ON ai_gateway.customer_entitlements (genfity_user_id)
    WHERE pricing_group = 'payg_topup' AND status = 'active';

-- +goose Down
DROP INDEX IF EXISTS ai_gateway.customer_entitlements_credit_package_unique;
DROP INDEX IF EXISTS ai_gateway.customer_entitlements_payg_topup_unique;
