package tasks

import (
	"context"
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

// Repo is postgres repo.
type Repo struct {
	resolver repository.Resolver
}

// New creates new repo.
func New(r repository.Resolver) *Repo {
	return &Repo{resolver: r}
}

// Insert inserts new task into repo.
func (r *Repo) Insert(ctx context.Context, t domaintasks.Task, idempotencyKey *string) error {
	q := queries.New(r.resolver.Resolve(ctx))
	if _, err := q.CreateTask(ctx, toRow(t, idempotencyKey)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return fmt.Errorf("insert task %s: %w", t.ID(), domaintasks.ErrConflict)
		}
		return fmt.Errorf("insert task %s: %w", t.ID(), err)
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

// translate maps driver errors to domain sentinels so on layer above this one
// has to import pgx.
func translate(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domaintasks.ErrNotFound
	}
	return err
}
