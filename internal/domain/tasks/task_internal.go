package tasks

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// FromPersistence rebuilds a Task from raw column values. Used ONLY by
// internal/repository/tasks. Not part of the public API.
func FromPersistence(id uuid.UUID, status Status, handler string, payload json.RawMessage, createdAt time.Time) Task {
	return Task{id: id, status: status, handler: handler, payload: payload, createdAt: createdAt}
}
