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
)

func TestToOASTask(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	task := domaintasks.Submit(id, "echo", json.RawMessage(`{"msg":"hi"}`), createdAt)

	dto, err := toOASTask(task)
	require.NoError(t, err)

	assert.Equal(t, id, dto.ID)
	assert.Equal(t, oas.TaskStatusPending, dto.Status)
	assert.True(t, createdAt.Equal(dto.CreatedAt))
}

func TestToOASTaskJSONRoundTrip(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	createdAt := time.Now().UTC().Truncate(time.Second)
	task := domaintasks.Submit(id, "echo", json.RawMessage(`{"msg":"hi"}`), createdAt)

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
			task := domaintasks.FromPersistence(
				uuid.Must(uuid.NewV7()), status, "echo",
				json.RawMessage(`{}`), time.Now().UTC(),
			)

			dto, err := toOASTask(task)
			require.NoError(t, err)

			assert.NoError(t, dto.Validate())
		})
	}
}
