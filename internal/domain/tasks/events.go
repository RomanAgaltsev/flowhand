package tasks

import (
	"time"

	"github.com/google/uuid"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
)

// Submitted is emitted when a task enters the system. The name suffix is the
// wire schema version - bump it, do not mutate the fields of a shipped event.
type Submitted struct {
	TaskID    uuid.UUID
	Handler   string
	ocurredAt time.Time
}

// compile-time proof the aggregate's events satisfy the domain contract.
var _ domain.Event = Submitted{}

// NewSubmitted creates new submitted task.
func NewSubmitted(t Task, now time.Time) Submitted {
	return Submitted{
		TaskID:    t.ID(),
		Handler:   t.Handler(),
		ocurredAt: now,
	}
}

// EventName returns event name.
func (e Submitted) EventName() string { return "task.submitted.v1" }

// OccurredAt returns time of event ocurrence.
func (e Submitted) OccurredAt() time.Time { return e.ocurredAt }
