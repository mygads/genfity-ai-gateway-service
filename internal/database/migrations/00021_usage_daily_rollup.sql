-- +goose Up
--
-- usage_daily_rollup: pre-aggregated daily snapshot of usage_ledger.
--
-- Why: usage_ledger keeps only 7 days of raw per-request rows (a daily
-- rollup+prune job rolls older days into this table, then deletes the
-- raw rows). Without a snapshot the admin usage dashboard's 30d/90d/all
-- ranges would silently drop everything older than 7 days and the counts
-- would be wrong. This table preserves the aggregate numbers forever at a
-- tiny fraction of the row count.
--
-- Safety: this is a pure analytics snapshot. It is NEVER read on the
-- billing/quota hot path — credit balance lives in
-- customer_entitlements.credit_balance, quota in quota_counters. Pruning
-- raw usage_ledger rows after rolling them up here cannot affect any
-- user's balance.
--
-- Grain: one row per (UTC day x every dimension the dashboards group by).
-- The dimension tuple is the primary key so the rollup job can UPSERT
-- idempotently (ON CONFLICT ... DO UPDATE adds the measures), making a
-- re-run safe. Empty string (not NULL) is used for absent dimensions so
-- they participate in the PK (NULLs are not equal in a unique index).
--
-- Not rolled up: latency percentiles (p50/p95/p99) are not additive, so
-- LatencyStats stays raw-only — a 7-day operational window, which is
-- acceptable.
CREATE TABLE IF NOT EXISTS ai_gateway.usage_daily_rollup (
    day             date          NOT NULL,
    genfity_user_id text          NOT NULL DEFAULT '',
    public_model    text          NOT NULL DEFAULT '',
    billing_mode    text          NOT NULL DEFAULT '',
    status          text          NOT NULL DEFAULT '',
    pricing_group   text          NOT NULL DEFAULT '',
    router_prefix   text          NOT NULL DEFAULT '',
    error_code      text          NOT NULL DEFAULT '',
    request_count   bigint        NOT NULL DEFAULT 0,
    input_tokens    bigint        NOT NULL DEFAULT 0,
    output_tokens   bigint        NOT NULL DEFAULT 0,
    total_tokens    bigint        NOT NULL DEFAULT 0,
    total_cost      numeric(18,6) NOT NULL DEFAULT 0,
    amount_credits  numeric(18,4) NOT NULL DEFAULT 0,
    created_at      timestamptz   NOT NULL DEFAULT now(),
    updated_at      timestamptz   NOT NULL DEFAULT now(),
    PRIMARY KEY (day, genfity_user_id, public_model, billing_mode, status, pricing_group, router_prefix, error_code)
);

-- Range scans by day for dashboard `since` filters.
CREATE INDEX IF NOT EXISTS usage_daily_rollup_day_idx
    ON ai_gateway.usage_daily_rollup (day);
-- Per-user dashboards (billing-detail "today"/period rollups).
CREATE INDEX IF NOT EXISTS usage_daily_rollup_user_day_idx
    ON ai_gateway.usage_daily_rollup (genfity_user_id, day);

-- +goose Down
DROP TABLE IF EXISTS ai_gateway.usage_daily_rollup;
