-- name: CreateTask :exec
-- Every value comes from the aggregate, created_at and status included: the
-- response to a submit and a later read of the same row must agree.
INSERT INTO tasks (
    id, idempotency_key, payload, status, handler,
    tenant_id, priority, earliest_at, attempt, max_attempts,
    shard_id, cancel_requested, created_at, updated_at, trace_id
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10,
    $11, $12, $13, $14, $15
);

-- name: GetTaskByID :one
SELECT * FROM tasks WHERE id = $1;

-- name: GetTaskForUpdate :one
-- Row-locks for a read-modify-write. Only meaningful inside a transaction:
-- on the bare pool the lock evaporates the moment the statement returns.
SELECT * FROM tasks WHERE id = $1 FOR UPDATE;

-- name: UpdateTaskStatus :execrows
-- The fenced status write. :execrows is load-bearing: the row count IS the
-- fence result - 0 means the stamped lease_epoch no longer matches, ownership
-- moved and the caller must abort without emitting events.
-- lease_epoch is NULLable (never dispatched). NULL never matches, so an
-- unleased row can never be transitioned through this query - by design.
-- A terminal status here fires trg_tasks_archive, which moves the row out of
-- tasks within this same statement.
UPDATE tasks
SET status = $2, earliest_at = $3, attempt = $4, updated_at = now()
WHERE id = $1 AND lease_epoch = $5;

-- name: InsertIdempotencyKey :exec
INSERT INTO idempotency_keys (tenant_id, handler, idempotency_key, task_id, payload_hash)
VALUES ($1, $2, $3, $4, $5);

-- name: GetIdempotencyRecord :one
-- The ledger replay: what a key resolves to once its insert has collided.
-- Keyed by the full primary key, so the read is scoped exactly as the gate is.
SELECT task_id, payload_hash FROM idempotency_keys
WHERE tenant_id = $1 AND handler = $2 AND idempotency_key = $3;

-- name: CreateAttempt :exec
INSERT INTO task_attempts (
    id, task_id, attempt, worker_id, status,
    started_at, finished_at, last_heartbeat, error_class, error_message
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8, $9, $10
);

-- name: UpsertAttempt :exec
-- The attempt row, written by PersistTransition: INSERT when the transition
-- opened the attempt (dispatch), UPDATE when it closed it (result).
-- On conflict the close writes status, finished_at, error_class and
-- error_message. It deliberately leaves started_at, worker_id, last_heartbeat,
-- progress_pct and error_stack alone: those belong to dispatch and heartbeats.
INSERT INTO task_attempts (
    id, task_id, attempt, worker_id, status,
    started_at, finished_at, last_heartbeat, progress_pct,
    error_class, error_message, error_stack
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO UPDATE SET
    status        = EXCLUDED.status,
    finished_at   = EXCLUDED.finished_at,
    error_class   = EXCLUDED.error_class,
    error_message = EXCLUDED.error_message;

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

-- name: GetArchivedTaskByID :one
-- The read fallback for a task that has reached a terminal status: the archive
-- trigger moves the row here in the same statement. Every partition's (id, finished_at)
-- primary key leads with id. id alone is not unique across partitions, so the
-- newest row wins: a future replay of a dead-lettered id must not read an old one.
SELECT * FROM tasks_archive WHERE id = $1 ORDER BY finished_at DESC LIMIT 1;
