package tasks

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/repository"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
)

// uniqueViolation is Postgres SQLSTATE 23505.
const uniqueViolation = "23505"

// Repo is the Postgres-backed task repository. It resolves its DBTX per call
// so the same instance works inside a transaction or on the bare pool,
// depending on what the Resolver finds bound to the context.
type Repo struct {
	resolver repository.Resolver
}

// New returns a task repository that resolves its connection through r.
func New(r repository.Resolver) *Repo {
	return &Repo{resolver: r}
}

// Insert persists a new task, its attempt history, and - when a key is supplied
// - its ledger row, in whatever transaction the Resolver hands back, so they
// commit together. The ledger's composite PK (tenant_id, handler,
// idempotency_key) is the durable dedup floor; tasks carries no uniqueness of
// its own since the archive trigger would evaporate it on completion.
func (r *Repo) Insert(ctx context.Context, t domaintasks.Task, idempotencyKey *string) error {
	q := queries.New(r.resolver.Resolve(ctx))
	if _, err := q.CreateTask(ctx, toRow(t, idempotencyKey)); err != nil {
		return fmt.Errorf("insert task %s: %w", t.ID(), err)
	}
	// Empty on the submit path - a freshly-submitted task has no attempts. The
	// loop is here so the aggregate and the row never disagree once a caller
	// inserts a task that already has history.
	for _, a := range toAttemptRows(t) {
		if _, err := q.CreateAttempt(ctx, a); err != nil {
			return fmt.Errorf("insert attempt %d of task %s: %w", a.Attempt, t.ID(), err)
		}
	}
	if idempotencyKey == nil {
		return nil
	}
	sum := sha256.Sum256(t.Payload())
	err := q.InsertIdempotencyKey(ctx, queries.InsertIdempotencyKeyParams{
		TenantID:       defaultTenantID,
		Handler:        t.Handler().Name(),
		IdempotencyKey: *idempotencyKey,
		TaskID:         t.ID(),
		PayloadHash:    sum[:],
	})
	if isUniqueViolation(err) {
		return fmt.Errorf("insert task %s: %w", t.ID(), domaintasks.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("insert idempotency key: %w", err)
	}
	return nil
}

// Get returns a task from repo by id, with its attempt history.
func (r *Repo) Get(ctx context.Context, id uuid.UUID) (domaintasks.Task, error) {
	q := queries.New(r.resolver.Resolve(ctx))
	row, err := q.GetTaskByID(ctx, id)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("get task %s: %w", id, translate(err))
	}
	return r.hydrate(ctx, q, row)
}

// GetByIdempotencyKey returns a task from repo by idempotency key.
func (r *Repo) GetByIdempotencyKey(ctx context.Context, key string) (domaintasks.Task, error) {
	q := queries.New(r.resolver.Resolve(ctx))
	row, err := q.GetTaskByIdempotencyKey(ctx, &key)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("get task by idempotency key: %w", translate(err))
	}
	return r.hydrate(ctx, q, row)
}

// GetForUpdate returns the task with its attempt history, row-locked until the
// surrounding transaction ends. Outside a transaction the lock evaporates when
// the statement returns - the port documents this. The repo cannot enforce it.
func (r *Repo) GetForUpdate(ctx context.Context, id uuid.UUID) (domaintasks.Task, error) {
	q := queries.New(r.resolver.Resolve(ctx))
	row, err := q.GetTaskForUpdate(ctx, id)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("get task %s for update: %w", id, translate(err))
	}
	return r.hydrate(ctx, q, row)
}

// PersistTransition writes the attempt row and the fenced status update.
// fenceEpoch is the epoch stamped AT DISPATCH and echoed by the worker - never
// a fresh lookup. Returns rows affected by the fenced UPDATE:
// 0 means the epoch moved and the caller must abort WITHOUT emitting events.
// On 0 the attempt row is skipped too: no audit record for a write we did
// not win.
func (r *Repo) PersistTransition(ctx context.Context, t domaintasks.Task, fenceEpoch int64) (int64, error) {
	q := queries.New(r.resolver.Resolve(ctx))

	rows, err := q.UpdateTaskStatus(ctx, queries.UpdateTaskStatusParams{
		ID:         t.ID(),
		Status:     string(t.Status()),
		EarliestAt: t.EarliestAt(),
		Attempt:    int32(t.AttemptCount()),
		LeaseEpoch: &fenceEpoch,
	})
	if err != nil {
		return 0, fmt.Errorf("persist transition of task %s: %w", t.ID(), err)
	}
	if rows == 0 {
		return 0, nil // fence rejected, caller branches on this
	}

	// The transition touched only the LAST attempt (Start appends one,
	// finishOpenAttempt closes one). Write exactly that row.
	if attempts := t.Attempts(); len(attempts) > 0 {
		n := int32(len(attempts))
		last := attempts[n-1]
		if err := q.UpsertAttempt(ctx, queries.UpsertAttemptParams{
			ID:            last.ID(),
			TaskID:        t.ID(),
			Attempt:       n,
			WorkerID:      last.WorkerID(),
			Status:        string(last.Status()),
			StartedAt:     last.StartedAt(),
			FinishedAt:    last.FinishedAt(),
			LastHeartbeat: last.LastHeartbeat(),
			ErrorClass:    ref(last.ErrorClass()),
			ErrorMessage:  ref(last.ErrorMessage()),
		}); err != nil {
			return rows, fmt.Errorf("upsert attempt %d of task %s: %w", n, t.ID(), err)
		}
	}
	return rows, nil
}

// errNotImplementedCE6 keeps the ports complete before Part C exists; the
// fenced, ceiling-bounded SQL lands with CE6.
var errNotImplementedCE6 = errors.New("not implemented: lands with CE6")

func (r *Repo) ExtendLease(ctx context.Context, id uuid.UUID, until time.Time, fenceEpoch int64, ceiling time.Duration) (int64, error) {
	return 0, errNotImplementedCE6
}

func (r *Repo) Heartbeat(ctx context.Context, taskID uuid.UUID, progress *float32, now time.Time) error {
	return errNotImplementedCE6
}

// hydrate loads a task row's attempts and rebuilds the aggregate. Two queries
// rather than a join: the attempt list is unbounded in principle and a join
// would repeat every task column per attempt. Both run on the same DBTX, so
// inside a transaction they see one consistent snapshot.
func (r *Repo) hydrate(ctx context.Context, q *queries.Queries, row queries.Task) (domaintasks.Task, error) {
	attempts, err := q.ListTaskAttempts(ctx, row.ID)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("list attempts of task %s: %w", row.ID, translate(err))
	}
	return toDomain(row, attempts)
}

// translate maps driver errors to domain sentinels so no layer above this one
// has to import pgx.
func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domaintasks.ErrNotFound
	}
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
