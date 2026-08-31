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

-- name: CreateAttempt :one
INSERT INTO task_attempts (
    id, task_id, attempt, worker_id, status,
    started_at, finished_at, last_heartbeat, error_class, error_message
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10
)
RETURNING *;

-- name: ListTaskAttempts :many
-- Ordered by attempt so the aggregate rebuilds its history in the order it
-- happened; Task.finishOpenAttempt only ever looks at the last element.
SELECT * FROM task_attempts WHERE task_id = $1 ORDER BY attempt;
