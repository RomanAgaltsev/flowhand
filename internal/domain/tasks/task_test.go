package tasks

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestSubmit(t *testing.T) {
	// UUIDv7 to match what the Commander mints, and a UTC instant truncated to
	// timestamptz resolution: time.Now() carries a monotonic reading that
	// reflect.DeepEqual compares as part of the struct, so a bare Now() makes
	// this test pass for reasons that stop holding once a real database is in
	// the loop.
	id := uuid.Must(uuid.NewV7())
	handler := "echo"
	payload := json.RawMessage(`{"msg":"hello"}`)
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	task := Submit(id, handler, payload, createdAt)

	assert.Equal(t, id, task.ID())
	assert.Equal(t, StatusPending, task.Status())
	assert.Equal(t, handler, task.Handler())
	assert.JSONEq(t, `{"msg":"hello"}`, string(task.Payload()))
	// Compare instants, not structs: DeepEqual on time.Time also compares the
	// *Location pointer, so the same instant in two zones would fail.
	assert.True(t, createdAt.Equal(task.CreatedAt()))
}
