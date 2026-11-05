package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/go-faster/jx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
)

// fakeCatalog stands in for the worker registry. Going through the real
// NewHandler rather than the persistence backdoor keeps these tests honest
// about how a Handler reaches the aggregate on a write path.
type fakeCatalog struct{}

func (fakeCatalog) Has(string) bool { return true }

// submitted builds a pending task the way the Commander does.
func submitted(t *testing.T, id uuid.UUID, payload json.RawMessage, now time.Time) domaintasks.Task {
	t.Helper()
	handler, err := domaintasks.NewHandler("echo", fakeCatalog{})
	require.NoError(t, err)
	task, err := domaintasks.Submit(id, handler, 0, payload, 1, now)
	require.NoError(t, err)
	return task
}

func TestToOASTask(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	task := submitted(t, id, json.RawMessage(`{"msg":"hi"}`), createdAt)

	dto, err := toOASTask(task)
	require.NoError(t, err)

	assert.Equal(t, id, dto.ID)
	assert.Equal(t, oas.TaskStatusPending, dto.Status)
	assert.True(t, createdAt.Equal(dto.CreatedAt))
}

func TestToOASTaskJSONRoundTrip(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	createdAt := time.Now().UTC().Truncate(time.Second)
	task := submitted(t, id, json.RawMessage(`{"msg":"hi"}`), createdAt)

	want, err := toOASTask(task)
	require.NoError(t, err)
	require.NoError(t, want.Validate())

	var e jx.Encoder
	want.Encode(&e)

	var got oas.Task
	require.NoError(t, got.Decode(jx.DecodeBytes(e.Bytes())))

	assert.Equal(t, want.ID, got.ID)
	assert.Equal(t, want.Status, got.Status)
	assert.True(t, want.CreatedAt.Equal(got.CreatedAt))
}

func TestToOASTaskStatusIsAlwaysValid(t *testing.T) {
	for _, status := range []domaintasks.Status{
		domaintasks.StatusPending,
		domaintasks.StatusRunning,
		domaintasks.StatusRetryScheduled,
		domaintasks.StatusSucceeded,
		domaintasks.StatusFailed,
		domaintasks.StatusCanceled,
		domaintasks.StatusDeadLettered,
	} {
		t.Run(string(status), func(t *testing.T) {
			now := time.Now().UTC()
			task := domaintasks.FromPersistence(
				uuid.Must(uuid.NewV7()),
				status,
				domaintasks.HandlerFromPersistence("echo"),
				0,
				json.RawMessage(`{}`),
				nil,
				0,
				now,
				1,
				now,
			)

			dto, err := toOASTask(task)
			require.NoError(t, err)

			assert.NoError(t, dto.Validate())
		})
	}
}

// A row reads back in the connection's time zone and a fresh submit is in UTC, so
// without this one instant reaches the wire spelled two ways, and a client comparing
// a replay's created_at to the original sees them differ.
func TestToOASTaskCreatedAtIsUTC(t *testing.T) {
	east := time.FixedZone("UTC+3", 3*60*60)
	at := time.Date(2026, 11, 5, 15, 0, 0, 0, east)

	dto, err := toOASTask(submitted(t, uuid.Must(uuid.NewV7()), json.RawMessage(`{}`), at))
	require.NoError(t, err)
	assert.Equal(t, time.UTC, dto.CreatedAt.Location())

	dto, err = toOASTaskView(task.TaskView{Status: "pending", CreatedAt: at})
	require.NoError(t, err)
	assert.Equal(t, time.UTC, dto.CreatedAt.Location())
	assert.True(t, at.Equal(dto.CreatedAt), "the instant must not change")
}
