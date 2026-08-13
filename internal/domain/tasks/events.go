package tasks

import (
	"time"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
	"github.com/google/uuid"
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

func NewSubmitted(t Task, now time.Time) Submitted {
	return Submitted{
		TaskID:    t.ID(),
		Handler:   t.Handler(),
		ocurredAt: now,
	}
}

func (e Submitted) EventName() string    { return "task.submitted.v1" }
func (e Submitted) OccuredAt() time.Time { return e.ocurredAt }
