-- +goose Up
CREATE SCHEMA IF NOT EXISTS ai_gateway;

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE ai_gateway.subscription_plan_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_code text NOT NULL UNIQUE,
    display_name text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    monthly_price numeric(18, 6) NOT NULL DEFAULT 0,
    currency text NOT NULL DEFAULT 'IDR',
    quota_tokens_monthly bigint,
    rate_limit_rpm integer,
    rate_limit_tpm integer,
    concurrent_limit integer,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    synced_from_genfity_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ai_gateway.customer_entitlements (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    genfity_user_id text NOT NULL,
    genfity_tenant_id text,
    plan_code text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    period_start timestamptz,
    period_end timestamptz,
    quota_tokens_monthly bigint,
    balance_snapshot numeric(18, 6),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_from_genfity_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT customer_entitlements_plan_code_fkey FOREIGN KEY (plan_code) REFERENCES ai_gateway.subscription_plan_snapshots(plan_code)
);

CREATE UNIQUE INDEX customer_entitlements_user_plan_idx ON ai_gateway.customer_entitlements (genfity_user_id, plan_code);
CREATE INDEX customer_entitlements_user_status_idx ON ai_gateway.customer_entitlements (genfity_user_id, status);
CREATE INDEX customer_entitlements_status_period_end_idx ON ai_gateway.customer_entitlements (status, period_end);

CREATE TABLE ai_gateway.api_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    genfity_user_id text NOT NULL,
    genfity_tenant_id text,
    name text NOT NULL,
    key_prefix text NOT NULL UNIQUE,
    key_hash text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    last_used_at timestamptz,
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

CREATE INDEX api_keys_user_status_idx ON ai_gateway.api_keys (genfity_user_id, status);
CREATE INDEX api_keys_tenant_status_idx ON ai_gateway.api_keys (genfity_tenant_id, status);

CREATE TABLE ai_gateway.ai_models (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    public_model text NOT NULL UNIQUE,
    display_name text NOT NULL,
    description text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active',
    context_window integer,
    supports_streaming boolean NOT NULL DEFAULT true,
    supports_tools boolean NOT NULL DEFAULT false,
    supports_vision boolean NOT NULL DEFAULT false,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ai_gateway.ai_model_prices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id uuid NOT NULL REFERENCES ai_gateway.ai_models(id) ON DELETE CASCADE,
    input_price_per_1m numeric(18, 6) NOT NULL DEFAULT 0,
    output_price_per_1m numeric(18, 6) NOT NULL DEFAULT 0,
    cached_price_per_1m numeric(18, 6),
    reasoning_price_per_1m numeric(18, 6),
    currency text NOT NULL DEFAULT 'IDR',
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ai_model_prices_active_model_idx ON ai_gateway.ai_model_prices (model_id) WHERE active;

CREATE TABLE ai_gateway.router_instances (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code text NOT NULL UNIQUE,
    public_base_url text,
    internal_base_url text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    encrypted_api_key text,
    health_status text,
    last_health_check_at timestamptz,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ai_gateway.ai_model_routes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id uuid NOT NULL REFERENCES ai_gateway.ai_models(id) ON DELETE CASCADE,
    router_instance_code text NOT NULL REFERENCES ai_gateway.router_instances(code),
    router_model text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ai_model_routes_active_model_idx ON ai_gateway.ai_model_routes (model_id) WHERE status = 'active';
CREATE INDEX ai_model_routes_router_idx ON ai_gateway.ai_model_routes (router_instance_code, status);

CREATE TABLE ai_gateway.usage_ledger (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id text NOT NULL UNIQUE,
    genfity_user_id text NOT NULL,
    genfity_tenant_id text,
    api_key_id uuid REFERENCES ai_gateway.api_keys(id),
    public_model text NOT NULL,
    router_model text,
    router_instance_code text,
    prompt_tokens bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    total_tokens bigint NOT NULL DEFAULT 0,
    cached_tokens bigint NOT NULL DEFAULT 0,
    reasoning_tokens bigint NOT NULL DEFAULT 0,
    input_cost numeric(18, 6) NOT NULL DEFAULT 0,
    output_cost numeric(18, 6) NOT NULL DEFAULT 0,
    total_cost numeric(18, 6) NOT NULL DEFAULT 0,
    status text NOT NULL,
    error_code text,
    latency_ms integer,
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX usage_ledger_user_started_idx ON ai_gateway.usage_ledger (genfity_user_id, started_at DESC);
CREATE INDEX usage_ledger_tenant_started_idx ON ai_gateway.usage_ledger (genfity_tenant_id, started_at DESC);
CREATE INDEX usage_ledger_api_key_started_idx ON ai_gateway.usage_ledger (api_key_id, started_at DESC);

CREATE TABLE ai_gateway.quota_counters (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    genfity_user_id text NOT NULL,
    genfity_tenant_id text,
    period_start timestamptz NOT NULL,
    period_end timestamptz NOT NULL,
    tokens_used bigint NOT NULL DEFAULT 0,
    tokens_reserved bigint NOT NULL DEFAULT 0,
    request_count bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (genfity_user_id, period_start, period_end)
);

CREATE TABLE ai_gateway.request_logs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id text NOT NULL UNIQUE,
    genfity_user_id uuid,
    genfity_tenant_id uuid,
    api_key_id uuid,
    method text NOT NULL,
    path text NOT NULL,
    status_code integer,
    error_code text,
    latency_ms integer,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX request_logs_created_idx ON ai_gateway.request_logs (created_at DESC);

CREATE TABLE ai_gateway.sync_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sync_type text NOT NULL,
    status text NOT NULL,
    item_count integer NOT NULL DEFAULT 0,
    error_message text,
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

INSERT INTO ai_gateway.router_instances (code, public_base_url, internal_base_url, status)
VALUES ('ai-core1', 'https://ai-core1.genfity.com', 'http://ai-core1-9router:20128', 'active')
ON CONFLICT (code) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS ai_gateway.sync_runs;
DROP TABLE IF EXISTS ai_gateway.request_logs;
DROP TABLE IF EXISTS ai_gateway.quota_counters;
DROP TABLE IF EXISTS ai_gateway.usage_ledger;
DROP TABLE IF EXISTS ai_gateway.ai_model_routes;
DROP TABLE IF EXISTS ai_gateway.router_instances;
DROP TABLE IF EXISTS ai_gateway.ai_model_prices;
DROP TABLE IF EXISTS ai_gateway.ai_models;
DROP TABLE IF EXISTS ai_gateway.api_keys;
DROP TABLE IF EXISTS ai_gateway.customer_entitlements;
DROP TABLE IF EXISTS ai_gateway.subscription_plan_snapshots;
DROP SCHEMA IF EXISTS ai_gateway;
