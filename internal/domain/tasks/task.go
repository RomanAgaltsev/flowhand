package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
)

// Task is the aggregate root for the tasks bounded context.
type Task struct {
	id          uuid.UUID
	status      Status
	handler     Handler
	priority    Priority
	payload     json.RawMessage
	attempts    []Attempt
	leaseEpoch  LeaseEpoch
	earliestAt  time.Time
	maxAttempts uint8
	createdAt   time.Time

	pendingEvents []domain.Event
}

// Status is the status of a task.
type Status string

const (
	// StatusPending is "pending".
	StatusPending Status = "pending"

	// StatusRunning is "running".
	StatusRunning Status = "running"

	// StatusRetryScheduled is "retry_scheduled".
	StatusRetryScheduled Status = "retry_scheduled"

	// StatusSucceeded is "succeeded".
	StatusSucceeded Status = "succeeded"

	// StatusFailed is "failed".
	StatusFailed Status = "failed"

	// StatusCanceled is "canceled".
	StatusCanceled Status = "canceled"

	// StatusDeadLettered is "dead_lettered".
	StatusDeadLettered Status = "dead_lettered"
)

// Submit constructs a freshly-submitted task.
func Submit(
	id uuid.UUID,
	handler Handler,
	priority Priority,
	payload json.RawMessage,
	maxAttempts uint8,
	now time.Time,
) (Task, error) {
	if maxAttempts == 0 {
		return Task{}, ErrInvalidMaxAttempts
	}
	if payload == nil {
		return Task{}, ErrNilPayload
	}

	t := Task{
		id:          id,
		status:      StatusPending,
		handler:     handler,
		priority:    priority,
		payload:     payload,
		maxAttempts: maxAttempts,
		earliestAt:  now,
		createdAt:   now,
	}
	t.pendingEvents = append(t.pendingEvents, NewSubmitted(t, now))
	return t, nil
}

// ID returns tasks id.
func (t Task) ID() uuid.UUID { return t.id }

// Status returns tasks status.
func (t Task) Status() Status { return t.status }

// Handler returns tasks handler.
func (t Task) Handler() Handler { return t.handler }

// Priority returns tasks priority.
func (t Task) Priority() Priority { return t.priority }

// Payload returns tasks payload.
func (t Task) Payload() json.RawMessage { return t.payload }

// LeaseEpoch returns tasks leaseEpoch.
func (t Task) LeaseEpoch() LeaseEpoch { return t.leaseEpoch }

// EarliestAt returns the instant before which the task must not be dequeued.
func (t Task) EarliestAt() time.Time { return t.earliestAt }

// MaxAttempts returns tasks maxAttempts.
func (t Task) MaxAttempts() uint8 { return t.maxAttempts }

// CreatedAt returns tasks createdAt.
func (t Task) CreatedAt() time.Time { return t.createdAt }

// Attempts returns a copy: the slice header is the aggregate boundary, and
// handing out the original lets a caller write t.attempts[0] from outside.
//
// The clone is shallow, which is safe because Attempt's nullable-time getters
// hand back copies rather than the pointers they hold — see Attempt.FinishedAt.
func (t Task) Attempts() []Attempt { return slices.Clone(t.attempts) }

var (
	// ErrInvalidTransition is returned when a method is called from a status
	// the transition table does not permit. It is always wrapped with the
	// current status and the attempted method — the caller's log line is
	// useless without them.
	ErrInvalidTransition = errors.New("invalid state transition")

	// ErrAttemptsExhausted is distinct from ErrInvalidTransition on purpose: the table permits
	// running → retry_scheduled, so this is not a caller bug. The Commander
	// branches on it to dead-letter instead of retrying.
	ErrAttemptsExhausted = errors.New("max attempts exhausted")
)

// ensure is the transition guard. It runs BEFORE any mutation in every method,
// which is what makes "an illegal transition leaves the state untouched" true
// by construction rather than by review.
func (t Task) ensure(method string, from ...Status) error {
	if slices.Contains(from, t.status) {
		return nil
	}
	return fmt.Errorf("%s from %s: %w", method, t.status, ErrInvalidTransition)
}

// finishOpenAttempt stamps the in-flight attempt, if there is one.
//
// The second guard is load-bearing: Cancel is legal from retry_scheduled, where
// ScheduleRetry already closed the last attempt. Writing to it again would
// overwrite why attempt N failed with the cancellation.
func (t *Task) finishOpenAttempt(now time.Time, status AttemptStatus, errorClass, errorMessage string) {
	if len(t.attempts) == 0 {
		return
	}
	i := len(t.attempts) - 1
	if t.attempts[i].finishedAt != nil {
		return // already closed by a previous transition
	}
	t.attempts[i].status = status
	t.attempts[i].finishedAt = &now
	t.attempts[i].errorClass = errorClass
	t.attempts[i].errorMessage = errorMessage
}

// AttemptCount is derived from the slice rather than stored. A counter field
// would be a second source of truth, and Step 6's parity property (Started
// events == AttemptCount) exists to catch exactly that drift.
//
// The clamp is unreachable — Start is only legal from pending/retry_scheduled,
// and ScheduleRetry refuses once the budget is spent, so len(attempts) never
// exceeds maxAttempts, itself a uint8. It is here so the conversion is provably
// safe to gosec (G115) and to a reader, rather than safe by argument.
func (t Task) AttemptCount() uint8 {
	n := len(t.attempts)
	if n > math.MaxUint8 {
		return math.MaxUint8
	}
	return uint8(n)
}

// IsTerminal reports whether the task has reached a status it can never leave.
//
// The terminal set is listed explicitly and everything else defaults to false.
// Go gives no exhaustiveness check on a string enum and the
// `exhaustive` linter is not enabled, so adding a *terminal* status means
// editing this switch, and schema.md's archive trigger has to agree with it.
func (t Task) IsTerminal() bool {
	switch t.status {
	case StatusSucceeded, StatusFailed, StatusCanceled, StatusDeadLettered:
		return true
	default:
		return false
	}
}

// CanRetry reports whether the task has attempt budget left.
//
// The IsTerminal guard is redundant when ScheduleRetry calls this — ensure()
// has already rejected terminal states — but CanRetry is exported, and the
// Commander calls it to choose between scheduling a retry and dead-lettering.
// A terminal task has no budget however few attempts it used.
func (t Task) CanRetry() bool {
	return !t.IsTerminal() && t.AttemptCount() < t.maxAttempts
}

// Start opens an attempt and moves the task to running. Legal from pending and
// from retry_scheduled — the two states a scheduler dispatches from.
//
// leaseUntil is accepted but not stored: the lease lives on the row, and CE6 is
// the task that gives the aggregate a lease field. Taking it now fixes the
// signature so CE6 does not have to change every call site.
func (t *Task) Start(attemptID uuid.UUID, workerID string, _ /* leaseUntil */, now time.Time) error {
	if err := t.ensure("start", StatusPending, StatusRetryScheduled); err != nil {
		return err
	}

	t.status = StatusRunning
	t.attempts = append(t.attempts, Attempt{
		id:        attemptID,
		status:    AttemptRunning,
		workerID:  workerID,
		startedAt: now,
	})
	t.pendingEvents = append(t.pendingEvents, NewStarted(*t, now))

	return nil
}

// Complete moves a running task to succeeded and closes its open attempt.
func (t *Task) Complete(result json.RawMessage, now time.Time) error {
	if err := t.ensure("complete", StatusRunning); err != nil {
		return err
	}
	t.status = StatusSucceeded
	t.finishOpenAttempt(now, AttemptSucceeded, "", "")
	t.pendingEvents = append(t.pendingEvents, NewSucceeded(*t, result, now))
	return nil
}

// Fail moves a running task to the TERMINAL failed status. This is the
// non-retryable path only: a transient failure calls ScheduleRetry, which never
// passes through `failed`, because the archive trigger treats it as terminal
// and would move the row out mid-retry (schema.md, 2026-07-04 review F9).
func (t *Task) Fail(errorClass, errorMessage string, now time.Time) error {
	if err := t.ensure("fail", StatusRunning); err != nil {
		return err
	}
	t.status = StatusFailed
	t.finishOpenAttempt(now, AttemptFailed, errorClass, errorMessage)
	t.pendingEvents = append(t.pendingEvents, NewFailed(*t, errorClass, errorMessage, now))
	return nil
}

// ScheduleRetry parks the task for a later attempt after a *transient* failure.
// This is the path: running → retry_scheduled directly, never through the
// terminal `failed` status, because the archive trigger would move the row out
// mid-retry. The failure itself is recorded on the attempt.
func (t *Task) ScheduleRetry(errorClass, errorMessage string, nextAt, now time.Time) error {
	if err := t.ensure("schedule retry", StatusRunning); err != nil {
		return err
	}
	if !t.CanRetry() {
		return fmt.Errorf("schedule retry after %d of %d attempts: %w",
			t.AttemptCount(), t.maxAttempts, ErrAttemptsExhausted)
	}

	t.status = StatusRetryScheduled
	t.earliestAt = nextAt
	t.finishOpenAttempt(now, AttemptFailed, errorClass, errorMessage)
	t.pendingEvents = append(t.pendingEvents, NewRetryScheduled(*t, nextAt, now))
	return nil
}

// Cancel moves a live task to canceled. Legal from every non-terminal status;
// a second Cancel returns ErrInvalidTransition and emits nothing.
func (t *Task) Cancel(reason string, now time.Time) error {
	if err := t.ensure("cancel", StatusPending, StatusRunning, StatusRetryScheduled); err != nil {
		return err
	}
	t.status = StatusCanceled
	t.finishOpenAttempt(now, AttemptFailed, "canceled", reason)
	t.pendingEvents = append(t.pendingEvents, NewCancelled(*t, reason, now))
	return nil
}

// PullEvents drains the pending-event buffer. Usage:
//
//	for ev := range task.PullEvents() { outbox.Append(ctx, ev) }
//
// The buffer is detached BEFORE iteration, not element-by-element during it. That is
// the whole trick, and it is why this is written out rather than left to you.
func (t *Task) PullEvents() iter.Seq[domain.Event] {
	events := t.pendingEvents
	t.pendingEvents = nil

	return func(yield func(domain.Event) bool) {
		for _, ev := range events {
			if !yield(ev) {
				return
			}
		}
	}
}
