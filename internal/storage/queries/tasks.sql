-- name: CreateTask :one
INSERT INTO tasks (id, idempotency_key, payload, handler)
VALUES ($1, $2, $3, $4)
RETURNING id, idempotency_key, payload, status, created_at, handler;

-- name: GetTaskByID :one
SELECT id, idempotency_key, payload, status, created_at, handler
FROM tasks
WHERE id = $1;

-- name: GetTaskByIdempotencyKey :one
SELECT id, idempotency_key, payload, status, created_at, handler
FROM tasks
WHERE idempotency_key = $1;
