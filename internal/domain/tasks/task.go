package tasks

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Task is the aggregate root for the tasks bounded context.
type Task struct {
	id        uuid.UUID
	status    Status
	handler   string
	payload   json.RawMessage
	createdAt time.Time
}

// Status is the status of a task.
type Status string

const (
	// StatusPending is "pending".
	StatusPending Status = "pending"

	// StatusRunning is "running".
	StatusRunning Status = "running"

	// StatusComplete is "complete".
	StatusComplete Status = "complete"

	// StatusFailed is "failed".
	StatusFailed Status = "failed"

	// StatusCanceled is "canceled".
	StatusCanceled Status = "canceled"
)

// Submit constructs a freshly-submitted task.
func Submit(id uuid.UUID, handler string, payload json.RawMessage, now time.Time) Task {
	return Task{
		id:        id,
		status:    StatusPending,
		handler:   handler,
		payload:   payload,
		createdAt: now,
	}
}

// ID returns tasks id.
func (t Task) ID() uuid.UUID { return t.id }

// Status returns tasks status.
func (t Task) Status() Status { return t.status }

// Handler returns tasks handler.
func (t Task) Handler() string { return t.handler }

// Payload returns tasks payload.
func (t Task) Payload() json.RawMessage { return t.payload }

// CreatedAt returns tasks createdAt.
func (t Task) CreatedAt() time.Time { return t.createdAt }
