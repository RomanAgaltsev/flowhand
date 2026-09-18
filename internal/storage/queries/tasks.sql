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

-- name: GetTaskForUpdate :one
-- Row-locks for a read-modify-write. Only meaningful inside a transaction:
-- on the bare pool the lock evaporates the moment the statement returns.
SELECT * FROM tasks WHERE id = $1 FOR UPDATE;

-- name: UpdateTaskStatus :execrows
-- The fenced status write. :execrows is load-bearing: the row count IS the
-- fence result - 0 means the stamped lease_eposh no longer matches, ownership
-- moved and the caller must abort without emitting events.
-- lease_epoch in NULLable (never dispatched). NULL never matches, so an
-- unleased row can never be transitioned through this query - by design.
-- A terminal status here fires trg_tasks_archive, which moves the row out of
-- tasks within this same statement.
UPDATE tasks
SET status = $2, earliest_at = $3, attempt = $4, updated_at = now()
WHERE id = $1 AND lease_epoch = $5;

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

-- name: UpsertAttempt :exec
-- The attempt row, written by PersistTransition: INSERT when the transition
-- opened the attempt (dispatch), UPDATE when it closed it (result).
-- On conflict only the lifecycle columns are touched: started_at, worker_id,
-- last_heartbeat and progress_pct belong to dispatch and heartbeats.
INSERT INTO task_attempts (
    id, task_id, attempt, worker_id, status,
    started_at, finished_at, last_heartbeat, progress_pct,
    error_class, error_message, error_stack
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO UPDATE SET
    status        = EXCLUDED.status,
    finished_at   = EXCLUDED.finished_at,
    error_class   = EXCLUDED.error_class,
    error_message = EXCLUDED.error_message,
    error_stack   = EXCLUDED.error_stack;

-- name: ListTaskAttempts :many
-- Ordered by attempt so the aggregate rebuilds its history in the order it
-- happened; Task.finishOpenAttempt only ever looks at the last element.
SELECT * FROM task_attempts WHERE task_id = $1 ORDER BY attempt;

-- name: AppendOutboxBatch :batchexec
-- One round trip for N events. This sits inside the same transaction as the
-- state change, so its latency is lock-hold time on the hot tasks row.
-- sent/created_at take their column defaults; envelope_ver comes from
-- domain.EnvelopeVersion on the Go side (the column is the one source).
INSERT INTO outbox_events (event_id, aggregate_id, type, envelope_ver, trace_id, payload)
VALUES ($1, $2, $3, $4, $5, $6);
