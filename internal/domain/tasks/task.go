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

type Status string

const (
	StatusPending  Status = "pending"
	StatusRunning  Status = "running"
	StatusComplete Status = "complete"
	StatusFailed   Status = "failed"
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

func (t Task) ID() uuid.UUID            { return t.id }
func (t Task) Status() Status           { return t.status }
func (t Task) Handler() string          { return t.handler }
func (t Task) Payload() json.RawMessage { return t.payload }
func (t Task) CreatedAt() time.Time     { return t.createdAt }
