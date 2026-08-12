package tasks

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
)

func TestToDomain(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	key := "idem-1"
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	row := queries.Task{
		ID:             id,
		IdempotencyKey: &key,
		Payload:        []byte(`{"msg":"hi"}`),
		Status:         "running",
		CreatedAt:      createdAt,
		Handler:        "echo",
	}

	task := toDomain(row)

	assert.Equal(t, id, task.ID())
	assert.Equal(t, domaintasks.StatusRunning, task.Status())
	assert.Equal(t, "echo", task.Handler())
	assert.Equal(t, json.RawMessage(`{"msg":"hi"}`), task.Payload())
	assert.True(t, createdAt.Equal(task.CreatedAt()))
}

func TestToRow(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	key := "idem-1"
	task := domaintasks.Submit(id, "echo", json.RawMessage(`{"msg":"hi"}`), time.Now())

	params := toRow(task, &key)

	assert.Equal(t, id, params.ID)
	assert.Equal(t, &key, params.IdempotencyKey)
	assert.Equal(t, "echo", params.Handler)
	assert.Equal(t, []byte(`{"msg":"hi"}`), params.Payload)
}

func TestToRowWithoutIdempotencyKey(t *testing.T) {
	task := domaintasks.Submit(uuid.Must(uuid.NewV7()), "echo", json.RawMessage(`{}`), time.Now())

	params := toRow(task, nil)

	assert.Nil(t, params.IdempotencyKey)
}

func TestMapperRoundTrip(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	want := domaintasks.Submit(id, "echo", json.RawMessage(`{"msg":"hi"}`), createdAt)

	params := toRow(want, nil)

	// Stands in for the RETURNING clause: the INSERT supplies id, payload and
	// handler; Postgres fills status and created_at from the 00001 defaults.
	row := queries.Task{
		ID:             params.ID,
		IdempotencyKey: params.IdempotencyKey,
		Payload:        params.Payload,
		Status:         string(domaintasks.StatusPending),
		CreatedAt:      createdAt,
		Handler:        params.Handler,
	}

	got := toDomain(row)

	assert.Equal(t, want.ID(), got.ID())
	assert.Equal(t, want.Status(), got.Status())
	assert.Equal(t, want.Handler(), got.Handler())
	assert.Equal(t, want.Payload(), got.Payload())
	assert.True(t, want.CreatedAt().Equal(got.CreatedAt()))
}
