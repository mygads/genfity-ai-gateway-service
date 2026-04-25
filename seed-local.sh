#!/usr/bin/env bash
set -euo pipefail

DB_URL="${DATABASE_URL:-postgresql://genfity:dbgenfity2026@localhost:5432/genfity_ai_gateway?sslmode=disable}"
USER_ID="${SEED_USER_ID:-11111111-1111-1111-1111-111111111111}"
TENANT_ID="${SEED_TENANT_ID:-}"
PLAN_CODE="${SEED_PLAN_CODE:-local-dev}"
MODEL_PUBLIC="${SEED_MODEL_PUBLIC:-genfity/test-model}"
MODEL_DISPLAY="${SEED_MODEL_DISPLAY:-Genfity Test Model}"
ROUTER_CODE="${SEED_ROUTER_CODE:-ai-core1}"
ROUTER_MODEL="${SEED_ROUTER_MODEL:-openai/gpt-4o-mini}"
API_KEY_NAME="${SEED_API_KEY_NAME:-Local Smoke Test Key}"
RAW_KEY="${SEED_RAW_KEY:-sk_genfity_live_localseed_1234567890abcdefghijklmnopqrstuvwxyz}"
KEY_PREFIX="${RAW_KEY:0:20}"
API_KEY_PEPPER="${API_KEY_PEPPER:-genfity-ai-gateway-dev-pepper-2026}"
export RAW_KEY API_KEY_PEPPER
KEY_HASH=$(python - <<'PY'
import hmac, hashlib, os
raw = os.environ['RAW_KEY'].encode()
pepper = os.environ['API_KEY_PEPPER'].encode()
print(hmac.new(pepper, raw, hashlib.sha256).hexdigest())
PY
)

psql "$DB_URL" <<SQL
INSERT INTO ai_gateway.subscription_plan_snapshots (
  plan_code, display_name, status, monthly_price, currency,
  quota_tokens_monthly, rate_limit_rpm, rate_limit_tpm, concurrent_limit, metadata
)
VALUES (
  '$PLAN_CODE', 'Local Dev Plan', 'active', 0, 'IDR',
  100000, 60, 120000, 5, '{}'::jsonb
)
ON CONFLICT (plan_code) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  status = EXCLUDED.status,
  quota_tokens_monthly = EXCLUDED.quota_tokens_monthly,
  rate_limit_rpm = EXCLUDED.rate_limit_rpm,
  rate_limit_tpm = EXCLUDED.rate_limit_tpm,
  concurrent_limit = EXCLUDED.concurrent_limit,
  updated_at = now();

INSERT INTO ai_gateway.customer_entitlements (
  genfity_user_id, genfity_tenant_id, plan_code, status,
  period_start, period_end, quota_tokens_monthly, balance_snapshot, metadata
)
VALUES (
  '$USER_ID', NULLIF('$TENANT_ID', '')::uuid, '$PLAN_CODE', 'active',
  now(), now() + interval '30 days', 100000, 0, '{}'::jsonb
)
ON CONFLICT (genfity_user_id, plan_code) DO UPDATE SET
  status = EXCLUDED.status,
  period_start = EXCLUDED.period_start,
  period_end = EXCLUDED.period_end,
  quota_tokens_monthly = EXCLUDED.quota_tokens_monthly,
  balance_snapshot = EXCLUDED.balance_snapshot,
  updated_at = now();

INSERT INTO ai_gateway.ai_models (
  public_model, display_name, description, status, supports_streaming, supports_tools, supports_vision, metadata
)
VALUES (
  '$MODEL_PUBLIC', '$MODEL_DISPLAY', 'Local seeded model', 'active', true, false, false, '{}'::jsonb
)
ON CONFLICT (public_model) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  description = EXCLUDED.description,
  status = EXCLUDED.status,
  updated_at = now();

UPDATE ai_gateway.ai_model_routes r
SET router_instance_code = '$ROUTER_CODE',
    router_model = '$ROUTER_MODEL',
    status = 'active',
    metadata = '{}'::jsonb
FROM ai_gateway.ai_models m
WHERE r.model_id = m.id AND m.public_model = '$MODEL_PUBLIC';

INSERT INTO ai_gateway.ai_model_routes (
  model_id, router_instance_code, router_model, status, metadata
)
SELECT m.id, '$ROUTER_CODE', '$ROUTER_MODEL', 'active', '{}'::jsonb
FROM ai_gateway.ai_models m
WHERE m.public_model = '$MODEL_PUBLIC'
  AND NOT EXISTS (
    SELECT 1
    FROM ai_gateway.ai_model_routes r
    WHERE r.model_id = m.id AND r.status = 'active'
  );

INSERT INTO ai_gateway.api_keys (
  genfity_user_id, genfity_tenant_id, name, key_prefix, key_hash, status, last_used_at
)
VALUES (
  '$USER_ID', NULLIF('$TENANT_ID', '')::uuid, '$API_KEY_NAME', '$KEY_PREFIX', '$KEY_HASH', 'active', now()
)
ON CONFLICT (key_prefix) DO UPDATE SET
  name = EXCLUDED.name,
  key_hash = EXCLUDED.key_hash,
  status = EXCLUDED.status,
  revoked_at = NULL,
  last_used_at = now();
SQL

printf 'Seed complete\n'
printf 'User ID: %s\n' "$USER_ID"
printf 'Public model: %s\n' "$MODEL_PUBLIC"
printf 'Router model: %s\n' "$ROUTER_MODEL"
printf 'API key: %s\n' "$RAW_KEY"
