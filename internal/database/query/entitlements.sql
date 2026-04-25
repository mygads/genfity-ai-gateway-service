-- name: UpsertCustomerEntitlement :one
INSERT INTO ai_gateway.customer_entitlements (
    id,
    genfity_user_id,
    genfity_tenant_id,
    plan_code,
    status,
    period_start,
    period_end,
    quota_tokens_monthly,
    balance_snapshot,
    metadata,
    updated_from_genfity_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now()
)
ON CONFLICT (genfity_user_id, plan_code) DO UPDATE SET
    genfity_tenant_id = EXCLUDED.genfity_tenant_id,
    status = EXCLUDED.status,
    period_start = EXCLUDED.period_start,
    period_end = EXCLUDED.period_end,
    quota_tokens_monthly = EXCLUDED.quota_tokens_monthly,
    balance_snapshot = EXCLUDED.balance_snapshot,
    metadata = EXCLUDED.metadata,
    updated_from_genfity_at = now(),
    updated_at = now()
RETURNING *;

-- name: GetActiveEntitlementByUser :one
SELECT *
FROM ai_gateway.customer_entitlements
WHERE genfity_user_id = $1 AND status = 'active'
ORDER BY updated_at DESC
LIMIT 1;

-- name: ListCustomerEntitlements :many
SELECT *
FROM ai_gateway.customer_entitlements
ORDER BY updated_at DESC;
