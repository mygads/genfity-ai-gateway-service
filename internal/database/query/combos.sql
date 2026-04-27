-- name: CreateVirtualCombo :one
INSERT INTO ai_gateway.virtual_combos (
    id, model_id, name, description, status, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING *;

-- name: UpdateVirtualCombo :one
UPDATE ai_gateway.virtual_combos
SET name = $2, description = $3, status = $4, metadata = $5, updated_at = now()
WHERE id = $1 RETURNING *;

-- name: DeleteVirtualCombo :exec
DELETE FROM ai_gateway.virtual_combos WHERE id = $1;

-- name: ListVirtualCombos :many
SELECT * FROM ai_gateway.virtual_combos ORDER BY name ASC;

-- name: GetVirtualComboByID :one
SELECT * FROM ai_gateway.virtual_combos WHERE id = $1 LIMIT 1;

-- name: GetVirtualComboByModelID :one
SELECT * FROM ai_gateway.virtual_combos WHERE model_id = $1 AND status = 'active' LIMIT 1;

-- name: CreateVirtualComboEntry :one
INSERT INTO ai_gateway.virtual_combo_entries (
    id, combo_id, priority, router_instance_code, router_model, trigger_on, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
) RETURNING *;

-- name: DeleteVirtualComboEntriesByComboID :exec
DELETE FROM ai_gateway.virtual_combo_entries WHERE combo_id = $1;

-- name: ListVirtualComboEntriesByComboID :many
SELECT * FROM ai_gateway.virtual_combo_entries WHERE combo_id = $1 ORDER BY priority ASC;
