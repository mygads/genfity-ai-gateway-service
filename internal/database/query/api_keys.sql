-- name: CreateAPIKey :one
INSERT INTO ai_gateway.api_keys (
    id,
    genfity_user_id,
    genfity_tenant_id,
    name,
    key_prefix,
    key_hash,
    status,
    expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: ListAPIKeysByUser :many
SELECT *
FROM ai_gateway.api_keys
WHERE genfity_user_id = $1
ORDER BY created_at DESC;

-- name: GetAPIKeyByPrefix :one
SELECT *
FROM ai_gateway.api_keys
WHERE key_prefix = $1
LIMIT 1;

-- name: MarkAPIKeyUsed :exec
UPDATE ai_gateway.api_keys
SET last_used_at = now()
WHERE id = $1;

-- name: RevokeAPIKey :exec
UPDATE ai_gateway.api_keys
SET status = 'revoked', revoked_at = now()
WHERE id = $1;
