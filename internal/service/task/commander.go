package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	idemp   IdempotencyStore
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
	idemp IdempotencyStore,
	catalog domaintasks.HandlerCatalog,
) *Commander {
	return &Commander{
		tasks:   tasks,
		outbox:  outbox,
		tx:      tx,
		idemp:   idemp,
		catalog: catalog,
		now:     time.Now,
		newID:   uuid.NewV7,
	}
}

// fastPath is BEFORE any id is minted and before the transaction.
// A hit returns the stored task and opens no transaction at all. The caller
// guards on a non-empty key, so this never sees "".
func (c *Commander) fastPath(ctx context.Context, rawKey string) (domaintasks.Task, bool) {
	key, err := domaintasks.NewIdempotencyKey(rawKey)
	if err != nil {
		// An invalid key cannot be looked up, so decline without consulting
		// Redis. Nothing upstream rejects it yet - the API checks only length and
		// Submit not at all - so it reaches the ledger uncached until C3.
		return domaintasks.Task{}, false
	}
	id, ok, err := c.idemp.Lookup(ctx, key)
	if err != nil || !ok {
		// The contract says err is always nil. Should a store ever break it, the
		// answer is still fail-through: a cache that errors is a cache that
		// missed. Not logged here - the store owns that log line.
		return domaintasks.Task{}, false
	}
	t, err := c.tasks.Get(ctx, id)
	if err != nil {
		// The cache pointed at a task that is not there. Treat it as a miss and
		// let the ledger decide - a stale entry must not 500 a submit.
		return domaintasks.Task{}, false
	}
	return t, true
}

// warmCache is the post-commit warm. Returns nothing, because the
// task is committed and the client's submit has already succeeded - there is
// no one left to report an error to. It logs instead.
//
// slog.Default rather than an injected logger: the Commander's shape is fixed
// by the design of record, and the composition root installs the obs handler
// as the default.
func (c *Commander) warmCache(ctx context.Context, rawKey string, taskID uuid.UUID) {
	key, err := domaintasks.NewIdempotencyKey(rawKey)
	if err != nil {
		// Submit does not validate the key, so the ledger can hold one the domain
		// rejects. Recording it under a zero key would alias every such submit.
		slog.WarnContext(ctx, "idempotency cache warm skipped: invalid key",
			"task_id", taskID, "err", err)
		return
	}
	if err := c.idemp.Record(ctx, key, taskID); err != nil {
		// Soft: the next duplicate misses the cache, pays one database round
		// trip and re-warms. Never retried, never returned.
		slog.WarnContext(ctx, "idempotency cache warm failed",
			"task_id", taskID, "err", err)
	}
}

// Submit stores a new task and its Submitted event. On an idempotency-key
// collision it returns the task already stored under that key.
//
// Returns the aggregate rather than a bare uuid.UUID: the API needs
// status and created_at for its 201 body, and a read-after-write to fetch them
// is both a wasted round trip and unusable on the replay path.
func (c *Commander) Submit(ctx context.Context, cmd SubmitCommand) (domaintasks.Task, error) {
	// Guarded here rather than inside fastPath so the call site says
	// out loud that a keyless submit never touches the cache.
	if cmd.IdempotencyKey != "" {
		if existing, ok := c.fastPath(ctx, cmd.IdempotencyKey); ok {
			return existing, nil
		}
	}

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
		if cmd.IdempotencyKey != "" {
			c.warmCache(ctx, cmd.IdempotencyKey, t.ID())
		}
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
