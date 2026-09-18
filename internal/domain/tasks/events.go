package tasks

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
)

// Every event below records a fact that already happened: it carries values,
// never a *Task. A pointer would describe whatever the task looks like when the
// consumer reads it rather than what was true at the transition.
//
// Type names are LOCAL (tasks.Submitted); wire names are GLOBAL
// (task.submitted.v1). The `.vN` suffix versions the PAYLOAD schema - bump it
// when a field changes meaning. The envelope is versioned separately by
// domain.EnvelopeVersion.

// Submitted is emitted when a task enters the system.
type Submitted struct {
	TaskID   uuid.UUID
	Handler  Handler
	Priority Priority
	At       time.Time
}

// NewSubmitted records that t was submitted at now.
func NewSubmitted(t Task, now time.Time) Submitted {
	return Submitted{
		TaskID:   t.ID(),
		Handler:  t.Handler(),
		Priority: t.Priority(),
		At:       now,
	}
}

// EventName returns the stable wire identifier.
func (e Submitted) EventName() string { return "task.submitted.v1" }

// OccurredAt returns the instant the transition happened.
func (e Submitted) OccurredAt() time.Time { return e.At }

// AggregateID returns the aggregate this event is about: the outbox's
// aggregate_id column and the Kafka partition key.
func (e Submitted) AggregateID() uuid.UUID { return e.TaskID }

// compile-time proof the aggregate's events satisfy the domain contract.
var _ domain.Event = Submitted{}

// Started is emitted when a worker takes a task and opens an attempt.
type Started struct {
	TaskID   uuid.UUID
	Handler  Handler
	Priority Priority
	At       time.Time
}

// NewStarted records that t started executing at now.
func NewStarted(t Task, now time.Time) Started {
	return Started{
		TaskID:   t.ID(),
		Handler:  t.Handler(),
		Priority: t.Priority(),
		At:       now,
	}
}

// EventName returns the stable wire identifier.
func (e Started) EventName() string { return "task.started.v1" }

// OccurredAt returns the instant the transition happened.
func (e Started) OccurredAt() time.Time { return e.At }

// AggregateID returns the aggregate this event is about: the outbox's
// aggregate_id column and the Kafka partition key.
func (e Started) AggregateID() uuid.UUID { return e.TaskID }

// compile-time proof the aggregate's events satisfy the domain contract.
var _ domain.Event = Started{}

// Succeeded is emitted when a task completes. It carries the result, because a
// consumer reading task.succeeded.v1 off the wire has no access to the row.
type Succeeded struct {
	TaskID   uuid.UUID
	Handler  Handler
	Priority Priority
	Result   json.RawMessage
	At       time.Time
}

// NewSucceeded records that t completed with result at now.
func NewSucceeded(t Task, result json.RawMessage, now time.Time) Succeeded {
	return Succeeded{
		TaskID:   t.ID(),
		Handler:  t.Handler(),
		Priority: t.Priority(),
		Result:   result,
		At:       now,
	}
}

// EventName returns the stable wire identifier.
func (e Succeeded) EventName() string { return "task.succeeded.v1" }

// OccurredAt returns the instant the transition happened.
func (e Succeeded) OccurredAt() time.Time { return e.At }

// AggregateID returns the aggregate this event is about: the outbox's
// aggregate_id column and the Kafka partition key.
func (e Succeeded) AggregateID() uuid.UUID { return e.TaskID }

// compile-time proof the aggregate's events satisfy the domain contract.
var _ domain.Event = Succeeded{}

// Failed carries the reason, not just the fact. A consumer reading
// task.failed.v1 off Kafka has no access to task_attempts.
//
// This is emitted only for a FINAL failure. A transient one goes
// running -> retry_scheduled and emits RetryScheduled instead.
type Failed struct {
	TaskID       uuid.UUID
	Handler      Handler
	Priority     Priority
	ErrorClass   string
	ErrorMessage string
	At           time.Time
}

// NewFailed records that t failed terminally at now.
func NewFailed(t Task, errorClass, errorMessage string, now time.Time) Failed {
	return Failed{
		TaskID:       t.ID(),
		Handler:      t.Handler(),
		Priority:     t.Priority(),
		ErrorClass:   errorClass,
		ErrorMessage: errorMessage,
		At:           now,
	}
}

// EventName returns the stable wire identifier.
func (e Failed) EventName() string { return "task.failed.v1" }

// OccurredAt returns the instant the transition happened.
func (e Failed) OccurredAt() time.Time { return e.At }

// AggregateID returns the aggregate this event is about: the outbox's
// aggregate_id column and the Kafka partition key.
func (e Failed) AggregateID() uuid.UUID { return e.TaskID }

// compile-time proof the aggregate's events satisfy the domain contract.
var _ domain.Event = Failed{}

// RetryScheduled says WHEN, or nobody downstream can tell a retry from a stall,
// and WHICH attempt just failed, or nobody can tell retry 1 from retry 5.
type RetryScheduled struct {
	TaskID   uuid.UUID
	Handler  Handler
	Priority Priority
	Attempt  uint8
	NextAt   time.Time
	At       time.Time
}

// NewRetryScheduled records that t was parked at now for another attempt at
// nextAt. Attempt is the count of attempts already made - the one that just
// failed - because the next one does not exist yet.
func NewRetryScheduled(t Task, nextAt, now time.Time) RetryScheduled {
	return RetryScheduled{
		TaskID:   t.ID(),
		Handler:  t.Handler(),
		Priority: t.Priority(),
		Attempt:  t.AttemptCount(),
		NextAt:   nextAt,
		At:       now,
	}
}

// EventName returns the stable wire identifier.
func (e RetryScheduled) EventName() string { return "task.retry_scheduled.v1" }

// OccurredAt returns the instant the transition happened.
func (e RetryScheduled) OccurredAt() time.Time { return e.At }

// AggregateID returns the aggregate this event is about: the outbox's
// aggregate_id column and the Kafka partition key.
func (e RetryScheduled) AggregateID() uuid.UUID { return e.TaskID }

// compile-time proof the aggregate's events satisfy the domain contract.
var _ domain.Event = RetryScheduled{}

// Cancelled is emitted when a task is cancelled before reaching a terminal
// state. Note the spelling split: the Go type and wire name use the double-L
// British form, while the STATUS is `canceled` to match context.Canceled.
type Cancelled struct {
	TaskID   uuid.UUID
	Handler  Handler
	Priority Priority
	Reason   string
	At       time.Time
}

// NewCancelled records that t was cancelled at now for reason.
func NewCancelled(t Task, reason string, now time.Time) Cancelled {
	return Cancelled{
		TaskID:   t.ID(),
		Handler:  t.Handler(),
		Priority: t.Priority(),
		Reason:   reason,
		At:       now,
	}
}

// EventName returns the stable wire identifier.
func (e Cancelled) EventName() string { return "task.cancelled.v1" }

// OccurredAt returns the instant the transition happened.
func (e Cancelled) OccurredAt() time.Time { return e.At }

// AggregateID returns the aggregate this event is about: the outbox's
// aggregate_id column and the Kafka partition key.
func (e Cancelled) AggregateID() uuid.UUID { return e.TaskID }

// compile-time proof the aggregate's events satisfy the domain contract.
var _ domain.Event = Cancelled{}
