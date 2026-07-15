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

func TestCreateTask_HappyPath(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	h := api.NewHandler(&fakeQuerier{row: queries.Task{
		ID: uuid.Must(uuid.NewV7()), Status: "pending", CreatedAt: now,
	}}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	resp, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{Handler: "echo"})
	require.NoError(t, err)
	assert.Equal(t, "pending", string(resp.Status))
}

func TestCreateTask_DBError(t *testing.T) {
	h := api.NewHandler(&fakeQuerier{err: errors.New("boom")}, slog.Default())
	_, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{Handler: "echo"})
	require.ErrorContains(t, err, "insert task")
}
