-- name: CreateUsageLedgerEntry :one
INSERT INTO ai_gateway.usage_ledger (
    id,
    request_id,
    genfity_user_id,
    genfity_tenant_id,
    api_key_id,
    public_model,
    router_model,
    router_instance_code,
    prompt_tokens,
    completion_tokens,
    total_tokens,
    cached_tokens,
    reasoning_tokens,
    input_cost,
    output_cost,
    total_cost,
    status,
    error_code,
    latency_ms,
    started_at,
    finished_at,
    metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
    $13, $14, $15, $16, $17, $18, $19, $20, $21, $22
)
RETURNING *;

-- name: ListUsageByUser :many
SELECT *
FROM ai_gateway.usage_ledger
WHERE genfity_user_id = $1
ORDER BY started_at DESC;

-- name: SumUsageByUser :one
SELECT
    count(*)::bigint AS request_count,
    coalesce(sum(prompt_tokens), 0)::bigint AS prompt_tokens,
    coalesce(sum(completion_tokens), 0)::bigint AS completion_tokens,
    coalesce(sum(total_tokens), 0)::bigint AS total_tokens,
    coalesce(sum(total_cost), 0)::numeric AS total_cost
FROM ai_gateway.usage_ledger
WHERE genfity_user_id = $1;
