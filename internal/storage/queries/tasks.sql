-- name: CreateTask :one
INSERT INTO tasks (
    id, idempotency_key, payload, handler,
    tenant_id, priority, earliest_at, attempt, max_attempts,
    shard_id, cancel_requested, updated_at
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8, $9,
    $10, $11, $12
)
RETURNING *;

-- name: GetTaskByID :one
SELECT * FROM tasks WHERE id = $1;

-- name: GetTaskByIdempotencyKey :one
SELECT * FROM tasks WHERE idempotency_key = $1;

-- name: InsertIdempotencyKey :exec
INSERT INTO idempotency_keys (tenant_id, handler, idempotency_key, task_id, payload_hash)
VALUES ($1, $2, $3, $4, $5);
