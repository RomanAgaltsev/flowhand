package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
)

// Submission defaults. Neither value is on the wire yet: CreateTaskRequest
// carries only handler, payload and idempotency_key. They live here rather than
// in the mapper because choosing a submission's budget is a write-side decision,
// and the per-handler spec that will supply them lands with the worker runtime.
const (
	// defaultPriority is the middle of nothing in particular: every task is
	// equal until a caller can say otherwise. Higher runs sooner.
	defaultPriority domaintasks.Priority = 0

	// defaultMaxAttempts is the attempt budget a handler gets unless its spec
	// says otherwise.
	defaultMaxAttempts uint8 = 25
)

// SubmitCommand is the write-side input for submitting a task: what the caller
// asked for, stripped of transport concerns. An empty IdempotencyKey means "no
// key": the Commander writes no ledger row for it.
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
	log     *slog.Logger
	now     func() time.Time
	newID   func() (uuid.UUID, error)
}

// NewCommander wires the write side over its persistence ports, with the real
// clock and a UUIDv7 generator. A nil log means slog.Default.
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
	log *slog.Logger,
) *Commander {
	if log == nil {
		log = slog.Default()
	}
	return &Commander{
		tasks:   tasks,
		outbox:  outbox,
		tx:      tx,
		idemp:   idemp,
		catalog: catalog,
		log:     log,
		now:     time.Now,
		newID:   uuid.NewV7,
	}
}

// ErrIdempotencyConflict is the class of every *ConflictError: an idempotency
// key reused with a different payload. The API layer maps it to 409.
var ErrIdempotencyConflict = errors.New("idempotency key reused with a different payload")

// ConflictError reports an idempotency key reused with a different payload. It
// carries the existing task's id, which the 409 body returns and unwraps to
// ErrIdempotencyConflict so a caller that needs only the class uses errors.Is.
type ConflictError struct {
	ExistingTaskID uuid.UUID
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s: existing task %s", ErrIdempotencyConflict, e.ExistingTaskID)
}

func (e *ConflictError) Unwrap() error { return ErrIdempotencyConflict }

// fastPath runs BEFORE any id is minted and before the transaction. A hit whose
// fingerprint matches returns the stored task and no transaction opens.
//
// Only the ledger may refuse a submit, so a hit whose fingerprint differs is
// confirmed with one primary-key read. The ledger agreeing with the cache is a
// *ConflictError, decided without calling Get because the answer does not need
// the task. The ledger agreeing with this submit means the cache was stale: it
// is re-warmed and the submit replays.
//
// A miss, a Lookup error, a failed ledger read and a hit that points at a task
// Get cannot find all return (zero, false, nil): let the transaction decide. A
// stale cache must never fail a submit.
func (c *Commander) fastPath(
	ctx context.Context, handler domaintasks.Handler, claim domaintasks.IdempotencyClaim,
) (domaintasks.Task, bool, error) {
	rec, ok, err := c.idemp.Lookup(ctx, handler, claim.Key)
	if err != nil || !ok {
		return domaintasks.Task{}, false, nil //nolint:nilerr // fail-through is the contract
	}

	if claim.Fingerprint != rec.Fingerprint {
		ledger, err := c.tasks.GetIdempotencyRecord(ctx, handler, claim.Key)
		if err != nil {
			// A miss means the ledger never saw the key: the cache outlived it, and
			// this submit creates. Any other failure is the transaction's to report.
			return domaintasks.Task{}, false, nil //nolint:nilerr // fail-through is the contract
		}
		if claim.Fingerprint != ledger.Fingerprint {
			return domaintasks.Task{}, false, &ConflictError{ExistingTaskID: ledger.TaskID}
		}
		rec = ledger
		c.warmCache(ctx, handler, claim.Key, ledger)
	}

	t, err := c.tasks.Get(ctx, rec.TaskID)
	if err != nil {
		return domaintasks.Task{}, false, nil //nolint:nilerr // fail-through is the contract
	}

	return t, true, nil
}

// replay answers a submit whose ledger insert collided. It runs OUTSIDE the
// aborted transaction - a unique violation poisons it, and any query on it
// fails with "current transaction is aborted". The ledger row, not the tasks
// table, is what it reads: the row outlives the task's move to the archive,
// and it carries the fingerprint the conflict check needs.
//
// GetIdempotencyRecord, then compare fingerprints: a mismatch is a
// *ConflictError and nothing else runs. A match: Get the task (it falls back
// to the archive), warmCache, return it. Either read failing is an error
// wrapped as "replay idempotent task: ..." - never a zero Task.
func (c *Commander) replay(
	ctx context.Context, handler domaintasks.Handler, claim domaintasks.IdempotencyClaim,
) (domaintasks.Task, error) {
	rec, err := c.tasks.GetIdempotencyRecord(ctx, handler, claim.Key)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("replay idempotent task: %w", err)
	}

	if claim.Fingerprint != rec.Fingerprint {
		return domaintasks.Task{}, &ConflictError{ExistingTaskID: rec.TaskID}
	}

	t, err := c.tasks.Get(ctx, rec.TaskID)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("replay idempotent task: %w", err)
	}

	c.warmCache(ctx, handler, claim.Key, rec)

	return t, nil
}

// warmCache is the post-commit warm, and the post-replay re-warm. Returns
// nothing, because the task is committed and the client's submit has already
// succeeded - there is no one left to report an error to. A Record error is
// logged and dropped.
//
// WithoutCancel because the submit has committed: a client that disconnects now
// must not cost the task its cache entry. The store bounds the call itself.
func (c *Commander) warmCache(
	ctx context.Context, handler domaintasks.Handler, key domaintasks.IdempotencyKey, rec domaintasks.IdempotencyRecord,
) {
	ctx = context.WithoutCancel(ctx)
	if err := c.idemp.Record(ctx, handler, key, rec); err != nil {
		// Soft: the next duplicate misses the cache, pays one database round
		// trip and re-warms. Never retried, never returned.
		c.log.WarnContext(ctx, "idempotency cache warm failed",
			"handler", handler.Name(),
			"idempotency_key", key,
			"task_id", rec.TaskID,
			"err", err)
	}
}

// Submit stores a new task and its Submitted event, and reports created=true.
// On an idempotency key it has seen before it returns the task already stored
// under that key with created=false - or, when the payload differs from the
// one that created it, a *ConflictError.
//
// The order is the contract - commander_test.go asserts it call by call:
//  1. NewHandler: an unknown handler is a bad request, and the handler is half
//     of the idempotency scope, so it comes before anything keyed.
//  2. Keyed submits only: NewIdempotencyKey (the backstop for callers that are
//     not the HTTP API), then NewPayloadFingerprint, then fastPath.
//  3. Mint the id, read the clock, domaintasks.Submit.
//  4. The transaction: Insert, then Append every pulled event in one batch.
//  5. Commit -> warmCache with the new task's record, created=true. Ledger
//     ErrConflict -> replay, created=false. Anything else -> an error.
//
// Returns the aggregate rather than a bare uuid.UUID: the API needs
// status and created_at for its body, and a read-after-write to fetch them
// is both a wasted round trip and unusable on the replay path.
func (c *Commander) Submit(ctx context.Context, cmd SubmitCommand) (domaintasks.Task, bool, error) {
	// First: an unknown handler is a bad request whatever else is true and the
	// handler is half of the idempotency key.
	handler, err := domaintasks.NewHandler(cmd.Handler, c.catalog)
	if err != nil {
		return domaintasks.Task{}, false, fmt.Errorf("submit task: %w", err)
	}

	// Stays nil for a keyless submit, which then writes no ledger row. Parsed
	// once, here, and handed down typed: the wire's pattern rejects a bad key
	// before it gets this far, and this is the backstop for every other caller.
	var claim *domaintasks.IdempotencyClaim
	if cmd.IdempotencyKey != "" {
		key, err := domaintasks.NewIdempotencyKey(cmd.IdempotencyKey)
		if err != nil {
			return domaintasks.Task{}, false, fmt.Errorf("submit task: %w", err)
		}
		fp, err := domaintasks.NewPayloadFingerprint(cmd.Payload)
		if err != nil {
			return domaintasks.Task{}, false, fmt.Errorf("submit task: %w", err)
		}
		claim = &domaintasks.IdempotencyClaim{Key: key, Fingerprint: fp}

		existing, hit, err := c.fastPath(ctx, handler, *claim)
		if err != nil {
			return domaintasks.Task{}, false, err
		}
		if hit {
			return existing, false, nil
		}
	}

	id, err := c.newID()
	if err != nil {
		return domaintasks.Task{}, false, fmt.Errorf("new id: %w", err)
	}

	// UTC strips the monotonic reading; Truncate drops what timestamptz cannot
	// store, so the aggregate, its events and the row all agree on the instant.
	now := c.now().UTC().Truncate(time.Microsecond)

	t, err := domaintasks.Submit(id, handler, defaultPriority, cmd.Payload, defaultMaxAttempts, now)
	if err != nil {
		return domaintasks.Task{}, false, fmt.Errorf("submit task: %w", err)
	}

	err = c.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := c.tasks.Insert(ctx, t, claim); err != nil {
			return err
		}

		// Drains the buffer - the aggregate is empty after this and everything
		// it emitted lands in the same transaction as the row.
		return c.outbox.Append(ctx, slices.Collect(t.PullEvents())...)
	})

	switch {
	case err == nil:
		if claim != nil {
			c.warmCache(ctx, handler, claim.Key, domaintasks.IdempotencyRecord{
				TaskID:      t.ID(),
				Fingerprint: claim.Fingerprint,
			})
		}
		return t, true, nil
	case claim != nil && errors.Is(err, domaintasks.ErrConflict):
		existing, err := c.replay(ctx, handler, *claim)
		if err != nil {
			return domaintasks.Task{}, false, err
		}
		return existing, false, nil
	default:
		return domaintasks.Task{}, false, fmt.Errorf("submit task: %w", err)
	}
}
