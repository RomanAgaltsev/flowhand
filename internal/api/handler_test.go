package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/api"
	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
)

// fakeCommander records the command it received so a test can assert the
// handler translates the request faithfully - and decides nothing else.
type fakeCommander struct {
	task domaintasks.Task
	err  error
	got  task.SubmitCommand
}

func (f *fakeCommander) Submit(_ context.Context, cmd task.SubmitCommand) (domaintasks.Task, error) {
	f.got = cmd
	return f.task, f.err
}

type fakeQuerier struct {
	view task.TaskView
	err  error
}

func (f *fakeQuerier) Get(_ context.Context, _ uuid.UUID) (task.TaskView, error) {
	return f.view, f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCreateTask_HappyPath(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	created := time.Now().UTC().Truncate(time.Microsecond)
	cmd := &fakeCommander{
		task: domaintasks.FromPersistence(id, domaintasks.StatusPending, "echo",
			json.RawMessage(`{}`), created),
	}
	h := api.NewHandler(cmd, &fakeQuerier{}, discardLogger())

	resp, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{
		Handler:        "echo",
		IdempotencyKey: oas.NewOptString("key-1"),
	})
	require.NoError(t, err)

	got, ok := resp.(*oas.Task) // narrow the union to the 201 case
	require.True(t, ok)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, oas.TaskStatusPending, got.Status)
	assert.True(t, created.Equal(got.CreatedAt))

	// The command carries what the request supplied - the handler translates,
	// it does not decide.
	assert.Equal(t, "echo", cmd.got.Handler)
	assert.Equal(t, "key-1", cmd.got.IdempotencyKey)
	assert.JSONEq(t, `{}`, string(cmd.got.Payload))
}

func TestCreateTask_AbsentKeyBecomesEmptyString(t *testing.T) {
	cmd := &fakeCommander{
		task: domaintasks.FromPersistence(uuid.Must(uuid.NewV7()), domaintasks.StatusPending,
			"echo", json.RawMessage(`{}`), time.Now().UTC()),
	}
	h := api.NewHandler(cmd, &fakeQuerier{}, discardLogger())

	_, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{Handler: "echo"})
	require.NoError(t, err)

	// "" is the service's contract for "no key"; turning it into a NULL column
	// is the Commander's job, not the handler's.
	assert.Empty(t, cmd.got.IdempotencyKey)
}

func TestCreateTask_MarshalsPayload(t *testing.T) {
	cmd := &fakeCommander{
		task: domaintasks.FromPersistence(uuid.Must(uuid.NewV7()), domaintasks.StatusPending,
			"echo", json.RawMessage(`{}`), time.Now().UTC()),
	}
	h := api.NewHandler(cmd, &fakeQuerier{}, discardLogger())

	req := &oas.CreateTaskRequest{Handler: "echo"}
	req.SetPayload(oas.NewOptCreateTaskRequestPayload(
		oas.CreateTaskRequestPayload{"a": []byte(`1`)},
	))

	_, err := h.CreateTask(context.Background(), req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":1}`, string(cmd.got.Payload))
}

func TestCreateTask_UnmappedStatusIs500(t *testing.T) {
	h := api.NewHandler(&fakeCommander{
		task: domaintasks.FromPersistence(uuid.Must(uuid.NewV7()), domaintasks.Status("quarantined"),
			"echo", json.RawMessage(`{}`), time.Now().UTC()),
	}, &fakeQuerier{}, discardLogger())

	_, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{Handler: "echo"})
	require.ErrorContains(t, err, "quarantined")
}

func TestGetTask_HappyPath(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	created := time.Now().UTC().Truncate(time.Microsecond)
	h := api.NewHandler(&fakeCommander{}, &fakeQuerier{view: task.TaskView{
		ID: id, Status: "pending", Handler: "echo", CreatedAt: created,
	}}, discardLogger())

	resp, err := h.GetTask(context.Background(), oas.GetTaskParams{ID: id})
	require.NoError(t, err)

	got, ok := resp.(*oas.Task)
	require.True(t, ok)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, oas.TaskStatusPending, got.Status)
	assert.True(t, created.Equal(got.CreatedAt))
}

func TestGetTask_RejectsStatusOutsideThePublishedEnum(t *testing.T) {
	// Domain, database and wire share one vocabulary, so the mapper is a rename
	// rather than a translation. What still has to hold is that a status the spec
	// does not publish never reaches a client: serving it would be a contract
	// violation that ogen's own Validate() would not catch on the way out.
	h := api.NewHandler(&fakeCommander{}, &fakeQuerier{view: task.TaskView{
		ID: uuid.Must(uuid.NewV7()), Status: "orphaned", CreatedAt: time.Now().UTC(),
	}}, discardLogger())

	_, err := h.GetTask(context.Background(), oas.GetTaskParams{ID: uuid.Must(uuid.NewV7())})
	require.Error(t, err, "an unpublished status must not be served as a Task")
	assert.Contains(t, err.Error(), "orphaned")
}

func TestGetTask_NotFound(t *testing.T) {
	h := api.NewHandler(&fakeCommander{}, &fakeQuerier{err: domaintasks.ErrNotFound}, discardLogger())

	resp, err := h.GetTask(context.Background(), oas.GetTaskParams{ID: uuid.Must(uuid.NewV7())})
	require.NoError(t, err)

	e, ok := resp.(*oas.GetTaskNotFound)
	require.True(t, ok)
	assert.Equal(t, oas.ErrorCodeTaskNotFound, e.Code)
}

func TestCreateTask_ServiceError(t *testing.T) {
	h := api.NewHandler(&fakeCommander{err: errors.New("boom")}, &fakeQuerier{}, discardLogger())

	resp, err := h.CreateTask(context.Background(), &oas.CreateTaskRequest{Handler: "echo"})
	// Returning the error - not a typed 500 - lets api.ErrorHandler render the
	// Error envelope and marks the span as failed.
	require.ErrorContains(t, err, "submit task")
	assert.Nil(t, resp)
}

func TestGetTask_UnmappedStatusIs500(t *testing.T) {
	h := api.NewHandler(&fakeCommander{}, &fakeQuerier{view: task.TaskView{
		ID: uuid.Must(uuid.NewV7()), Status: "quarantined",
	}}, discardLogger())

	_, err := h.GetTask(context.Background(), oas.GetTaskParams{ID: uuid.Must(uuid.NewV7())})
	require.ErrorContains(t, err, "quarantined")
}
