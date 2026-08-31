package tasks

import (
	"time"

	"github.com/google/uuid"
)

// AttemptStatus is the per-attempt outcome. Deliberately NOT tasks.Status: a
// per-attempt `failed` is ordinary and frequent, whereas on tasks it is
// terminal and archive-triggering (migrations/00003, task_attempts).
type AttemptStatus string

const (
	// AttemptRunning is an attempt a worker still holds.
	AttemptRunning AttemptStatus = "running"

	// AttemptSucceeded is an attempt that returned a result.
	AttemptSucceeded AttemptStatus = "succeeded"

	// AttemptFailed is an attempt that raised. Ordinary and frequent - it says
	// nothing about whether the TASK is finished.
	AttemptFailed AttemptStatus = "failed"
)

// Attempt is inside Task's aggregate boundary: it is created only by Task.Start
// and mutated only by Task's methods. Unexported fields are what enforce that -
// there is no exported constructor and no setter, so external code cannot
// manufacture one.
type Attempt struct {
	id            uuid.UUID
	status        AttemptStatus
	workerID      string
	startedAt     time.Time
	finishedAt    *time.Time
	lastHeartbeat *time.Time
	errorClass    string
	errorMessage  string
}

// AttemptFromPersistence rebuilds an attempt from a stored row. Exported for
// the same reason FromPersistence is: internal/repository/tasks is a different
// package, so an unexported constructor would be unreachable there. It performs
// no validation - the row went through Task.Start on the way in.
//
// This is NOT a general constructor. Nothing on a write path may call it;
// attempts are created by Task.Start.
func AttemptFromPersistence(
	id uuid.UUID,
	status AttemptStatus,
	workerID string,
	startedAt time.Time,
	finishedAt, lastHeartbeat *time.Time,
	errorClass, errorMessage string,
) Attempt {
	return Attempt{
		id:            id,
		status:        status,
		workerID:      workerID,
		startedAt:     startedAt,
		finishedAt:    copyTime(finishedAt),
		lastHeartbeat: copyTime(lastHeartbeat),
		errorClass:    errorClass,
		errorMessage:  errorMessage,
	}
}

// ID returns the attempt's identity.
func (a Attempt) ID() uuid.UUID { return a.id }

// Status returns the per-attempt outcome.
func (a Attempt) Status() AttemptStatus { return a.status }

// WorkerID returns the worker that held this attempt.
func (a Attempt) WorkerID() string { return a.workerID }

// StartedAt returns when the attempt opened.
func (a Attempt) StartedAt() time.Time { return a.startedAt }

// FinishedAt returns when the attempt closed, or nil while it is in flight.
//
// It returns a COPY of the instant, not the aggregate's own pointer. Value
// receivers and unexported fields would otherwise still leave the boundary
// open: handing back &a.finishedAt lets a caller write through it and mutate a
// closed attempt from outside the aggregate. The same applies to
// LastHeartbeat.
func (a Attempt) FinishedAt() *time.Time { return copyTime(a.finishedAt) }

// LastHeartbeat returns the most recent heartbeat, or nil if none. Copied for
// the reason given on FinishedAt.
func (a Attempt) LastHeartbeat() *time.Time { return copyTime(a.lastHeartbeat) }

// ErrorClass returns the failure taxonomy bucket, empty when the attempt did
// not fail.
func (a Attempt) ErrorClass() string { return a.errorClass }

// ErrorMessage returns the failure detail, empty when the attempt did not fail.
func (a Attempt) ErrorMessage() string { return a.errorMessage }

// IsOpen reports whether the attempt is still in flight.
func (a Attempt) IsOpen() bool { return a.finishedAt == nil }

// copyTime defensively copies a nullable instant so no pointer into an
// aggregate's attempt escapes it.
func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
