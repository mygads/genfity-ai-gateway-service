-- name: UpsertRouterInstance :one
INSERT INTO ai_gateway.router_instances (
    id,
    code,
    public_base_url,
    internal_base_url,
    status,
    encrypted_api_key,
    health_status,
    last_health_check_at,
    metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (code) DO UPDATE SET
    public_base_url = EXCLUDED.public_base_url,
    internal_base_url = EXCLUDED.internal_base_url,
    status = EXCLUDED.status,
    encrypted_api_key = EXCLUDED.encrypted_api_key,
    health_status = EXCLUDED.health_status,
    last_health_check_at = EXCLUDED.last_health_check_at,
    metadata = EXCLUDED.metadata
RETURNING *;

-- name: ListRouterInstances :many
SELECT *
FROM ai_gateway.router_instances
ORDER BY code ASC;

-- name: GetRouterInstanceByCode :one
SELECT *
FROM ai_gateway.router_instances
WHERE code = $1
LIMIT 1;
