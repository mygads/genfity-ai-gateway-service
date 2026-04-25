-- name: CreateSyncRun :one
INSERT INTO ai_gateway.sync_runs (
    id,
    sync_type,
    status,
    item_count,
    error_message,
    started_at,
    finished_at,
    metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: UpdateSyncRunFinished :exec
UPDATE ai_gateway.sync_runs
SET status = $2,
    item_count = $3,
    error_message = $4,
    finished_at = now()
WHERE id = $1;
