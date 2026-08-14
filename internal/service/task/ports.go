package task

import (
	"context"

	"github.com/google/uuid"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
	"github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
)

// TasksRepo is the persistence port for the Task aggregate.
// Implemented by internal/repository/tasks.
type TasksRepo interface {
	// Insert takes the idempotency key alongside the aggregate: the key is a
	// property of the submission, not of the task.
	// A nil key means "no key" - the partial unique index ignores NULLs.
	Insert(ctx context.Context, t tasks.Task, idempotencyKey *string) error
	Get(ctx context.Context, id uuid.UUID) (tasks.Task, error)
	GetByIdempotencyKey(ctx context.Context, key string) (tasks.Task, error)
}

// OutboxRepo persists domain events.
type OutboxRepo interface {
	Append(ctx context.Context, events ...domain.Event) error
}

// TxRunner runs fn inside a Postgres transaction.
type TxRunner interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}
