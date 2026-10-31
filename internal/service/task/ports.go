package task

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
	"github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
)

// TasksRepo is the persistence port for the Task aggregate.
// Implemented by internal/repository/tasks.
type TasksRepo interface {
	// Insert takes the idempotency key alongside the aggregate: the key is a
	// property of the submission, not of the task. A non-nil key also writes the
	// ledger row - key, task id, payload fingerprint - in the same transaction;
	// a key already in the ledger is ErrConflict. A nil key means "no key" and
	// writes no ledger row.
	Insert(ctx context.Context, t tasks.Task, idempotencyKey *string) error
	// Get finds a task wherever it lives: the hot table, or tasks_archive once
	// it has reached a terminal status. Only then is it ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (tasks.Task, error)

	// GetIdempotencyRecord reads the ledger row for (handler, key): the task the
	// first submit created and its payload fingerprint. ErrNotFound on a miss.
	// The ledger outlives the task row, so this answers after archival too.
	GetIdempotencyRecord(ctx context.Context, handler tasks.Handler, key tasks.IdempotencyKey) (tasks.IdempotencyRecord, error)

	// GetForUpdate row-locks the task for a read-modify-write. Callers MUST already be
	// inside WithinTx. Outside one it degrades to a plain read and releases the lock
	// immediately - no error, just a silently unlocked read-modify-write.
	// Hot table only, unlike Get: an archived task is terminal and has no
	// transition left to write.
	GetForUpdate(ctx context.Context, id uuid.UUID) (tasks.Task, error)

	// PersistTransition writes the attempt row and the fenced status update. Returns
	// rows affected by the fenced UPDATE: 0 means the epoch moved and the caller must
	// abort WITHOUT emitting events.
	PersistTransition(ctx context.Context, t tasks.Task, fenceEpoch int64) (int64, error)

	// ExtendLease pushes the lease deadline to until, fenced on the dispatch-time
	// epoch and bounded by ceiling (measured from the attempt's started_at, so a
	// handler cannot renew forever). Returns rows affected: 0 means stale epoch or
	// ceiling reached - a no-op, NOT an error.
	ExtendLease(ctx context.Context, id uuid.UUID, until time.Time, fenceEpoch int64, ceiling time.Duration) (int64, error)
}

// AttemptsRepo is the persistence port for per-attempt runtime facts (the
// heartbeat sink). Same concrete type as TasksRepo - internal/repository/tasks.
// The split is at the port: a consumer that needs one cannot reach the other.
type AttemptsRepo interface {
	// Heartbeat stamps last_heartbeat = now (and progress_pct when non-nil) on
	// the in-flight attempt row. Heartbeats land on task_attempts, never on
	// tasks - schema.md's rule that keeps the hot table's write rate
	// proportional to state transitions. Implemented with CE6.
	Heartbeat(ctx context.Context, taskID uuid.UUID, progress *float32, now time.Time) error
}

// OutboxRepo persists domain events.
type OutboxRepo interface {
	Append(ctx context.Context, events ...domain.Event) error
}

// TxRunner runs fn inside a Postgres transaction.
type TxRunner interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
