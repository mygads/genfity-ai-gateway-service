-- +goose Up
--
-- PRD v3 Phase 1B: per-model credit cost lookup table for the
-- credit_package billing schema. One row per CLIProxyAPI prefix/model
-- (e.g. "mtr/claude-opus-4.7"). Each request to that model debits
-- `credits_per_req` from the user's credit balance.
--
-- The is_free flag is a fast-path bypass — when true, no credit check
-- runs. Normally is_free is implied by credits_per_req=0, but having
-- an explicit flag avoids ambiguity when the model is free for one
-- promo period and then paid.
--
-- Source of truth lives in genfity-app's AiGatewayModelCreditCost
-- table; this is a synced read-replica that the gateway hot path
-- queries without crossing the network.
CREATE TABLE IF NOT EXISTS ai_gateway.model_credit_cost (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    full_model_id text NOT NULL UNIQUE,
    credits_per_req numeric(10, 4) NOT NULL DEFAULT 0,
    is_free boolean NOT NULL DEFAULT false,
    notes text,
    synced_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- Mirror the genfity-app DECIMAL(10,4) range with a sanity bound:
    -- credits per request must be non-negative.
    CONSTRAINT model_credit_cost_credits_nonneg CHECK (credits_per_req >= 0)
);

CREATE INDEX IF NOT EXISTS model_credit_cost_is_free_idx
    ON ai_gateway.model_credit_cost (is_free)
    WHERE is_free = true;

-- PRD v3 Phase 1B: PAYG top-up package catalog. Synced from genfity-app
-- so the gateway can validate checkout requests + record the rate that
-- was applied at purchase time (rate locking).
--
-- valid_from/valid_until support time-bounded promos. NULL on either
-- bound means open-ended. Status='active' + within validity window =
-- selectable at checkout. rate_usd_idr is denormalized = price_idr /
-- usd_amount; computed at insert/update time so admin UI doesn't have
-- to recompute.
CREATE TABLE IF NOT EXISTS ai_gateway.payg_topup_rate (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code text NOT NULL UNIQUE,
    display_name text NOT NULL,
    usd_amount numeric(18, 2) NOT NULL,
    price_idr numeric(18, 2) NOT NULL,
    rate_usd_idr numeric(18, 4) NOT NULL,
    status text NOT NULL DEFAULT 'active',
    sort_order integer NOT NULL DEFAULT 0,
    valid_from timestamptz,
    valid_until timestamptz,
    is_promo boolean NOT NULL DEFAULT false,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    synced_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT payg_topup_rate_usd_positive CHECK (usd_amount > 0),
    CONSTRAINT payg_topup_rate_idr_positive CHECK (price_idr > 0),
    CONSTRAINT payg_topup_rate_rate_positive CHECK (rate_usd_idr > 0)
);

CREATE INDEX IF NOT EXISTS payg_topup_rate_status_sort_idx
    ON ai_gateway.payg_topup_rate (status, sort_order);
CREATE INDEX IF NOT EXISTS payg_topup_rate_validity_idx
    ON ai_gateway.payg_topup_rate (valid_from, valid_until);

-- +goose Down
DROP TABLE IF EXISTS ai_gateway.payg_topup_rate;
DROP TABLE IF EXISTS ai_gateway.model_credit_cost;
