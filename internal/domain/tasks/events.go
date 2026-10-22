package tasks

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
)

// Kind identifies which transition an Event records. The zero value is
// invalid: an event that does not say what happened cannot be published.
type Kind uint8

// The six transitions a task can record. Adding one here without adding it to
// EventName's switch fails the exhaustive linter.
const (
	KindSubmitted Kind = iota + 1
	KindStarted
	KindSucceeded
	KindFailed
	KindRetryScheduled
	KindCancelled
)

// Event is the single domain event of the tasks aggregate. It records a fact
// that already happened: it carries values, never a *Task. A pointer would
// describe whatever the task looks like when the consumer reads it rather than
// what was true at the transition.
//
// The fields are unexported and the constructors below are the only way to
// build one, so an event cannot carry a field its Kind does not use.
//
// The Go type is LOCAL (tasks.Event). The wire names are GLOBAL
// (task.submitted.v1). The .vN suffix versions the PAYLOAD schema - bump it
// when a field changes meaning. The envelope is versioned separately by
// domain.EnvelopeVersion.
type Event struct {
	kind     Kind
	taskID   uuid.UUID
	handler  Handler
	priority Priority
	at       time.Time

	result       json.RawMessage // KindSucceeded
	errorClass   string          // KindFailed
	errorMessage string          // KindFailed
	attempt      uint8           // KindRetryScheduled
	nextAt       time.Time       // KindRetryScheduled
	reason       string          // KindCancelled
}

// compile-time proof the aggregate's event satisfies the domain contract.
var _ domain.Event = Event{}

// base copies the identifying fields every event carries off the aggregate.
func base(k Kind, t Task, now time.Time) Event {
	return Event{
		kind:     k,
		taskID:   t.ID(),
		handler:  t.Handler(),
		priority: t.Priority(),
		at:       now,
	}
}

// NewSubmitted records that t entered the system at now.
func NewSubmitted(t Task, now time.Time) Event { return base(KindSubmitted, t, now) }

// NewStarted records that a worker took t and opened an attempt at now.
func NewStarted(t Task, now time.Time) Event { return base(KindStarted, t, now) }

// NewSucceeded records that t completed with result at now. The result travels
// with the event because a consumer reading task.succeeded.v1 off the wire has
// no access to the row.
func NewSucceeded(t Task, result json.RawMessage, now time.Time) Event {
	e := base(KindSucceeded, t, now)
	e.result = result
	return e
}

// NewFailed records that t failed terminally at now. It carries the reason, not
// just the fact: a consumer reading task.failed.v1 off Kafka has no access to
// task_attempts.
//
// This is emitted only for a FINAL failure. A transient one goes
// running -> retry_scheduled and emits NewRetryScheduled instead.
func NewFailed(t Task, errorClass, errorMessage string, now time.Time) Event {
	e := base(KindFailed, t, now)
	e.errorClass = errorClass
	e.errorMessage = errorMessage
	return e
}

// NewRetryScheduled records that t was parked at now for another attempt at
// nextAt. It says WHEN, or nobody downstream can tell a retry from a stall, and
// WHICH attempt just failed, or nobody can tell retry 1 from retry 5. Attempt is
// the count of attempts already made - the one that just failed - because the
// next one does not exist yet.
func NewRetryScheduled(t Task, nextAt, now time.Time) Event {
	e := base(KindRetryScheduled, t, now)
	e.attempt = t.AttemptCount()
	e.nextAt = nextAt
	return e
}

// NewCancelled records that t was cancelled at now for reason, before reaching a
// terminal state. Note the spelling split: the Go constant and the wire name use
// the double-L British form, while the STATUS is `canceled` to match
// context.Canceled.
func NewCancelled(t Task, reason string, now time.Time) Event {
	e := base(KindCancelled, t, now)
	e.reason = reason
	return e
}

// EventName returns the stable wire identifier for this event's Kind, or "" for
// an unset Kind.
//
// Write this as a switch on e.kind and not as a lookup table: the exhaustive
// linter checks switches over Kind, so a seventh transition added without a name
// here fails CI. A map or array literal buys nothing and forfeits that.
//
// The six names are in this task's Design of record. They are a published
// contract - a typo here is a silently-dropped consumer, not a compile error.
func (e Event) EventName() string {
	switch e.kind {
	case KindSubmitted:
		return "task.submitted.v1"
	case KindStarted:
		return "task.started.v1"
	case KindSucceeded:
		return "task.succeeded.v1"
	case KindFailed:
		return "task.failed.v1"
	case KindRetryScheduled:
		return "task.retry_scheduled.v1"
	case KindCancelled:
		return "task.cancelled.v1"
	}
	return ""
}

// OccurredAt returns the instant the transition happened.
func (e Event) OccurredAt() time.Time { return e.at }

// AggregateID returns the aggregate this event is about: the outbox's
// aggregate_id column and the Kafka partition key.
func (e Event) AggregateID() uuid.UUID { return e.taskID }

// MarshalJSON emits the payload for this event's Kind and nothing else, so a
// submitted event never ships an empty reason. It is required rather than
// optional: the fields are unexported, and reflection would marshal them to {}.
//
// The keys are snake_case, matching the envelope that wraps them
// (internal/repository/outbox/mapper.go). The key order per Kind is in this
// task's Design of record, and it is pinned byte-for-byte by the goldens in
// Step 3. An unknown Kind must return an error, not empty bytes.
//
// This is a published format.
func (e Event) MarshalJSON() ([]byte, error) {
	// common is embedded in each variant below; encoding/json flattens its
	// fields first, so every payload opens with the same four keys.
	type common struct {
		TaskID   uuid.UUID `json:"task_id"`
		Handler  Handler   `json:"handler"`
		Priority Priority  `json:"priority"`
		At       time.Time `json:"at"`
	}
	c := common{TaskID: e.taskID, Handler: e.handler, Priority: e.priority, At: e.at}

	switch e.kind {
	case KindSubmitted, KindStarted:
		return json.Marshal(c)
	case KindSucceeded:
		return json.Marshal(struct {
			common
			Result json.RawMessage `json:"result"`
		}{c, e.result})
	case KindFailed:
		return json.Marshal(struct {
			common
			ErrorClass   string `json:"error_class"`
			ErrorMessage string `json:"error_message"`
		}{c, e.errorClass, e.errorMessage})
	case KindRetryScheduled:
		return json.Marshal(struct {
			common
			Attempt uint8     `json:"attempt"`
			NextAt  time.Time `json:"next_at"`
		}{c, e.attempt, e.nextAt})
	case KindCancelled:
		return json.Marshal(struct {
			common
			Reason string `json:"reason"`
		}{c, e.reason})
	}
	return nil, fmt.Errorf("marshal task event: unknown kind %d", e.kind)
}
