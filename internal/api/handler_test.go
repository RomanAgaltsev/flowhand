package api_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/RomanAgaltsev/flowhand/internal/api"
	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeQuerier answers with row/err by default. A test that needs one method to
// behave differently from another - the idempotency replay path calls
// CreateTask then GetTaskByIdempotencyKey - overrides just that method's func
// field.
type fakeQuerier struct {
	row queries.Task
	err error

	createFn   func(context.Context, queries.CreateTaskParams) (queries.Task, error)
	getByIDFn  func(context.Context, uuid.UUID) (queries.Task, error)
	getByKeyFn func(context.Context, *string) (queries.Task, error)
}

func (f *fakeQuerier) CreateTask(ctx context.Context, arg queries.CreateTaskParams) (queries.Task, error) {
	if f.createFn != nil {
		return f.createFn(ctx, arg)
	}
	return f.row, f.err
}

func (f *fakeQuerier) GetTaskByID(ctx context.Context, id uuid.UUID) (queries.Task, error) {
	if f.getByIDFn != nil {
		return f.getByIDFn(ctx, id)
	}
	return f.row, f.err
}

func (f *fakeQuerier) GetTaskByIdempotencyKey(ctx context.Context, idempotencyKey *string) (queries.Task, error) {
	if f.getByKeyFn != nil {
		return f.getByKeyFn(ctx, idempotencyKey)
	}
	return f.row, f.err
}

// uniqueViolation is the error Postgres returns when the partial unique index
// tasks_idempotency_key_uniq rejects a duplicate key. Only Code is read by the
// handler, but it must be a *pgconn.PgError for errors.As to match.
func uniqueViolation() error { return &pgconn.PgError{Code: "23505"} }

func TestCreateTask_HappyPath(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	h := api.NewHandler(&fakeQuerier{row: queries.Task{
		ID: uuid.Must(uuid.NewV7()), Status: "pending", CreatedAt: now,
	}}, discardLogger())

	resp, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{Handler: "echo"})
	require.NoError(t, err)
	task, ok := resp.(*oas.Task) // narrow the union to the 200 case
	require.True(t, ok)
	assert.Equal(t, "pending", string(task.Status))
}

func TestCreateTask_DBError(t *testing.T) {
	h := api.NewHandler(&fakeQuerier{err: errors.New("boom")}, discardLogger())
	_, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{Handler: "echo"})
	require.ErrorContains(t, err, "insert task")
}

func TestCreateTask_ReplaysOnIdempotencyConflict(t *testing.T) {
	stored := queries.Task{
		ID:        uuid.Must(uuid.NewV7()),
		Status:    "running",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	var gotKey *string
	h := api.NewHandler(&fakeQuerier{
		createFn: func(context.Context, queries.CreateTaskParams) (queries.Task, error) {
			return queries.Task{}, uniqueViolation()
		},
		getByKeyFn: func(_ context.Context, key *string) (queries.Task, error) {
			gotKey = key
			return stored, nil
		},
	}, discardLogger())

	resp, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{
		Handler:        "echo",
		IdempotencyKey: oas.NewOptString("key-1"),
	})
	require.NoError(t, err)

	task, ok := resp.(*oas.Task)
	require.True(t, ok)
	// The stored task is returned, not the freshly minted one - that is what
	// makes the second submit a replay rather than a new task.
	assert.Equal(t, stored.ID, task.ID)
	assert.Equal(t, "running", string(task.Status))
	require.NotNil(t, gotKey)
	assert.Equal(t, "key-1", *gotKey)
}

func TestCreateTask_ReplayLookupFails(t *testing.T) {
	h := api.NewHandler(&fakeQuerier{
		createFn: func(context.Context, queries.CreateTaskParams) (queries.Task, error) {
			return queries.Task{}, uniqueViolation()
		},
		getByKeyFn: func(context.Context, *string) (queries.Task, error) {
			return queries.Task{}, errors.New("connection refused")
		},
	}, discardLogger())

	resp, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{
		Handler:        "echo",
		IdempotencyKey: oas.NewOptString("key-1"),
	})
	// An unreadable row must surface as an error (rendered as a 500 by
	// api.ErrorHandler), never as a zero-value Task.
	require.ErrorContains(t, err, "replay idempotent task")
	assert.Nil(t, resp)
}

func TestCreateTask_ConflictWithoutKey(t *testing.T) {
	h := api.NewHandler(&fakeQuerier{
		createFn: func(context.Context, queries.CreateTaskParams) (queries.Task, error) {
			return queries.Task{}, uniqueViolation()
		},
		getByKeyFn: func(context.Context, *string) (queries.Task, error) {
			t.Fatal("must not look up a replay without an idempotency key")
			return queries.Task{}, nil
		},
	}, discardLogger())

	// No idempotency_key means the 23505 came from the UUIDv7 primary key, not
	// from tasks_idempotency_key_uniq - a genuine 500, not something to replay.
	_, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{Handler: "echo"})
	require.ErrorContains(t, err, "idempotency conflict without a key")
}

func TestCreateTask_NonUniqueViolationIsNotReplayed(t *testing.T) {
	h := api.NewHandler(&fakeQuerier{
		createFn: func(context.Context, queries.CreateTaskParams) (queries.Task, error) {
			return queries.Task{}, &pgconn.PgError{Code: "23503"} // foreign_key_violation
		},
		getByKeyFn: func(context.Context, *string) (queries.Task, error) {
			t.Fatal("only 23505 is a replay; every other SQLSTATE is a plain failure")
			return queries.Task{}, nil
		},
	}, discardLogger())

	_, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{
		Handler:        "echo",
		IdempotencyKey: oas.NewOptString("key-1"),
	})
	require.ErrorContains(t, err, "insert task")
}

func TestGetTask_HappyPath(t *testing.T) {
	id := uuid.Must(uuid.NewV7())

	now := time.Now().UTC().Truncate(time.Second)
	h := api.NewHandler(&fakeQuerier{row: queries.Task{
		ID: id, Status: "pending", CreatedAt: now,
	}}, discardLogger())

	resp, err := h.GetTask(context.Background(), oas.GetTaskParams{ID: id})
	require.NoError(t, err)
	task, ok := resp.(*oas.Task)
	require.True(t, ok)
	assert.Equal(t, id, task.ID)
	assert.Equal(t, "pending", string(task.Status))
}

func TestGetTask_NotFound(t *testing.T) {
	h := api.NewHandler(&fakeQuerier{err: pgx.ErrNoRows}, discardLogger())
	resp, err := h.GetTask(context.Background(), oas.GetTaskParams{ID: uuid.Must(uuid.NewV7())})
	require.NoError(t, err)
	e, ok := resp.(*oas.GetTaskNotFound)
	require.True(t, ok)
	assert.Equal(t, "404", e.Code)
}

func TestGetTask_DBError(t *testing.T) {
	h := api.NewHandler(&fakeQuerier{err: errors.New("connection refused")}, discardLogger())
	_, err := h.GetTask(context.Background(), oas.GetTaskParams{ID: uuid.Must(uuid.NewV7())})
	require.Error(t, err)
	assert.NotErrorIs(t, err, pgx.ErrNoRows) // proves it took the non-404 branch
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
