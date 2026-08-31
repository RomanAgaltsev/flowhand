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

// Submission defaults. Neither value is on the wire yet: CreateTaskRequest
// carries only handler, payload and idempotency_key. They live here rather than
// in the mapper - where max_attempts was hardcoded before D2 - because choosing
// a submission's budget is a write-side decision, and the per-handler spec that
// will supply them lands with the worker runtime.
const (
	// defaultPriority is the middle of nothing in particular: every task is
	// equal until a caller can say otherwise. Higher runs sooner.
	defaultPriority domaintasks.Priority = 0

	// defaultMaxAttempts is schema.md's per-HandlerSpec default.
	defaultMaxAttempts uint8 = 25
)

// SubmitCommand is the write-side input for submitting a task: what the caller
// asked for, stripped of transport concerns. An empty IdempotencyKey means "no
// key" - the Commander turns that into a NULL column so the partial unique
// index ignores it.
type SubmitCommand struct {
	Handler        string
	Payload        json.RawMessage
	IdempotencyKey string
}

// Commander is the write side of the task service. It owns the submit
// transaction - aggregate insert plus outbox append - and the idempotency
// replay decision. now and newID are injected so tests control both.
type Commander struct {
	tasks   TasksRepo
	outbox  OutboxRepo
	tx      TxRunner
	catalog domaintasks.HandlerCatalog
	now     func() time.Time
	newID   func() (uuid.UUID, error)
}

// NewCommander wires the write side over its persistence ports, with the real
// clock and a UUIDv7 generator.
//
// The catalog is what turns a handler STRING from the wire into a domain
// Handler. The domain declares that interface and the composition root supplies
// it, so the dependency arrow runs worker -> domain and the domain stays a leaf.
func NewCommander(
	tasks TasksRepo,
	outbox OutboxRepo,
	tx TxRunner,
	catalog domaintasks.HandlerCatalog,
) *Commander {
	return &Commander{
		tasks:   tasks,
		outbox:  outbox,
		tx:      tx,
		catalog: catalog,
		now:     time.Now,
		newID:   uuid.NewV7,
	}
}

// Submit stores a new task and its Submitted event. On an idempotency-key
// collision it returns the task already stored under that key.
//
// Returns the aggregate rather than a bare uuid.UUID: the API needs
// status and created_at for its 201 body, and a read-after-write to fetch them
// is both a wasted round trip and unusable on the replay path.
func (c *Commander) Submit(ctx context.Context, cmd SubmitCommand) (domaintasks.Task, error) {
	// Before the id, before the clock: an unknown handler is a bad request, and
	// minting an id for it would burn a UUID and read as a half-done submit.
	handler, err := domaintasks.NewHandler(cmd.Handler, c.catalog)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("submit task: %w", err)
	}

	id, err := c.newID()
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("new id: %w", err)
	}

	// UTC strips the monotonic reading, so the value compares cleanly against
	// what timestamptz gives back.
	now := c.now().UTC()

	t, err := domaintasks.Submit(id, handler, defaultPriority, cmd.Payload, defaultMaxAttempts, now)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("submit task: %w", err)
	}

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
		// Drains the buffer - the aggregate is empty after this, and everything
		// it emitted lands in the same transaction as the row.
		for ev := range t.PullEvents() {
			if err := c.outbox.Append(ctx, ev); err != nil {
				return err
			}
		}
		return nil
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
