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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeQuerier struct {
	row queries.Task
	err error
}

func (f *fakeQuerier) CreateTask(context.Context, queries.CreateTaskParams) (queries.Task, error) {
	return f.row, f.err
}

func (f *fakeQuerier) GetTaskByID(ctx context.Context, id uuid.UUID) (queries.Task, error) {
	return f.row, f.err
}

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
	h := api.NewHandler(&fakeQuerier{err: errors.New("boom")}, slog.Default())
	_, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{Handler: "echo"})
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
	e, ok := resp.(*oas.GetTaskInternalServerError)
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
