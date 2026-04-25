-- name: CreateModel :one
INSERT INTO ai_gateway.ai_models (
    id,
    public_model,
    display_name,
    description,
    status,
    context_window,
    supports_streaming,
    supports_tools,
    supports_vision,
    metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
RETURNING *;

-- name: ListModels :many
SELECT *
FROM ai_gateway.ai_models
ORDER BY display_name ASC;

-- name: GetModelByPublicModel :one
SELECT *
FROM ai_gateway.ai_models
WHERE public_model = $1
LIMIT 1;

-- name: UpsertModelPrice :one
INSERT INTO ai_gateway.ai_model_prices (
    id,
    model_id,
    input_price_per_1m,
    output_price_per_1m,
    cached_price_per_1m,
    reasoning_price_per_1m,
    currency,
    active
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
ON CONFLICT (id) DO UPDATE SET
    input_price_per_1m = EXCLUDED.input_price_per_1m,
    output_price_per_1m = EXCLUDED.output_price_per_1m,
    cached_price_per_1m = EXCLUDED.cached_price_per_1m,
    reasoning_price_per_1m = EXCLUDED.reasoning_price_per_1m,
    currency = EXCLUDED.currency,
    active = EXCLUDED.active
RETURNING *;

-- name: ListModelPrices :many
SELECT *
FROM ai_gateway.ai_model_prices
ORDER BY created_at DESC;

-- name: UpsertModelRoute :one
INSERT INTO ai_gateway.ai_model_routes (
    id,
    model_id,
    router_instance_code,
    router_model,
    status,
    metadata
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (id) DO UPDATE SET
    router_instance_code = EXCLUDED.router_instance_code,
    router_model = EXCLUDED.router_model,
    status = EXCLUDED.status,
    metadata = EXCLUDED.metadata
RETURNING *;

-- name: ListModelRoutes :many
SELECT *
FROM ai_gateway.ai_model_routes
ORDER BY created_at DESC;

-- name: GetActiveRouteByModelID :one
SELECT *
FROM ai_gateway.ai_model_routes
WHERE model_id = $1 AND status = 'active'
LIMIT 1;
