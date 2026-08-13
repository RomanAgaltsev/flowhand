package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
)

// SubmitCommand is the write-side input for submitting a task: what the caller
// asked for, stripped of transport concerns. An empty IdempotencyKey means "no
// key" — the Commander turns that into a NULL column so the partial unique
// index ignores it.
type SubmitCommand struct {
	Handler        string
	Payload        json.RawMessage
	IdempotencyKey string
}

// Commander is the write side of the task service. It owns the submit
// transaction — aggregate insert plus outbox append — and the idempotency
// replay decision. now and newID are injected so tests control both.
type Commander struct {
	tasks  TasksRepo
	outbox OutboxRepo
	tx     TxRunner
	now    func() time.Time
	newID  func() (uuid.UUID, error)
}

// NewCommander wires the write side over its persistence ports, with the real
// clock and a UUIDv7 generator.
func NewCommander(tasks TasksRepo, outbox OutboxRepo, tx TxRunner) *Commander {
	return &Commander{
		tasks:  tasks,
		outbox: outbox,
		tx:     tx,
		now:    time.Now,
		newID:  uuid.NewV7,
	}
}

// Submit stores a new task and its Submitted event. On an idempotency-key
// collision it returns the task already stored under that key.
//
// Returns the aggregate rather than a bare uuid.UUID: the API needs
// status and created_at for its 201 body, and a read-after-write to fetch them
// is both a wasted round trip and unusable on the replay path.
func (c *Commander) Submit(ctx context.Context, cmd SubmitCommand) (domaintasks.Task, error) {
	id, err := c.newID()
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("new id: %w", err)
	}

	// UTC strips the monotonic reading, so the value compares cleanly against
	// what timestamptz gives back.
	now := c.now().UTC()
	t := domaintasks.Submit(id, cmd.Handler, cmd.Payload, now)

	var key *string
	if cmd.IdempotencyKey != "" {
		// A pointer to "" is NOT the same as nil here: "" is a real value the
		// partial unique index enforces, so every keyless submit after the
		// first would collide.
		key = &cmd.IdempotencyKey
	}

	err = c.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := c.tasks.Insert(ctx, t, key); err != nil {
			return err
		}
		return c.outbox.Append(ctx, domaintasks.NewSubmitted(t, now))
	})
	switch {
	case err == nil:
		return t, nil
	case errors.Is(err, domaintasks.ErrConflict):
		if cmd.IdempotencyKey == "" {
			// 23505 with no key means the collision came from the UUIDv7
			// primary key, not tasks_idempotency_key_uniq. Nothing to replay -
			// this is a genuine failure. (Was TestCreateTask_ConflictWithoutKey.)
			return domaintasks.Task{}, fmt.Errorf("idempotency conflict without a key: %w", err)
		}
		// Deliberately OUTSIDE WithinTx. Once T1 makes this a real transaction,
		// a unique violation aborts it - any query on the same tx would fail
		// with "current transaction is aborted". The replay read needs a fresh one.
		existing, rerr := c.tasks.GetByIdempotencyKey(ctx, cmd.IdempotencyKey)
		if rerr != nil {
			// An unreadable row must surface as an error, never as a zero Task.
			return domaintasks.Task{}, fmt.Errorf("replay idempotent task: %w", rerr)
		}
		return existing, nil
	default:
		return domaintasks.Task{}, fmt.Errorf("submit task: %w", err)
	}
}
