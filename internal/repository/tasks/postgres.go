package tasks

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

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

// Insert persists a new task and, when a key is supplied, its ledger row — in
// whatever transaction the Resolver hands back, so the two commit together.
// The ledger's composite PK (tenant_id, handler, idempotency_key) is the durable
// dedup floor; `tasks` carries no uniqueness of its own since the archive trigger
// would evaporate it on completion.
func (r *Repo) Insert(ctx context.Context, t domaintasks.Task, idempotencyKey *string) error {
	q := queries.New(r.resolver.Resolve(ctx))
	if _, err := q.CreateTask(ctx, toRow(t, idempotencyKey)); err != nil {
		return fmt.Errorf("insert task %s: %w", t.ID(), err)
	}
	if idempotencyKey == nil {
		return nil
	}
	sum := sha256.Sum256(t.Payload())
	err := q.InsertIdempotencyKey(ctx, queries.InsertIdempotencyKeyParams{
		TenantID:       defaultTenantID,
		Handler:        t.Handler(),
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

// Get returns a task from repo by id.
func (r *Repo) Get(ctx context.Context, id uuid.UUID) (domaintasks.Task, error) {
	q := queries.New(r.resolver.Resolve(ctx))
	row, err := q.GetTaskByID(ctx, id)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("get task %s: %w", id, translate(err))
	}
	return toDomain(row), nil
}

// GetByIdempotencyKey returns a task from repo by idempotency key.
func (r *Repo) GetByIdempotencyKey(ctx context.Context, key string) (domaintasks.Task, error) {
	q := queries.New(r.resolver.Resolve(ctx))
	row, err := q.GetTaskByIdempotencyKey(ctx, &key)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("get task by idempotency key: %w", translate(err))
	}
	return toDomain(row), nil
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
