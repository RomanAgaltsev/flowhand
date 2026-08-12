package tasks

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestSubimt(t *testing.T) {
	id := uuid.New()
	handler := "echo"
	payload := json.RawMessage([]byte("hello"))
	createdAt := time.Now()

	task := Submit(id, handler, payload, createdAt)

	assert.Equal(t, id, task.ID())
	assert.Equal(t, StatusPending, task.Status())
	assert.Equal(t, handler, task.Handler())
	assert.Equal(t, payload, task.Payload())
	assert.Equal(t, createdAt, task.CreatedAt())
}
