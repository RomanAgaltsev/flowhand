-- name: CreateTask :one
INSERT INTO tasks (id, idempotency_key, payload)
VALUES ($1, $2, $3)
RETURNING id, idempotency_key, payload, status, created_at;

-- name: GetTaskByID :one
SELECT id, idempotency_key, payload, status, created_at
FROM tasks
WHERE id = $1;
