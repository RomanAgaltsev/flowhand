package tasks

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
)

// TestEventNames pins the exact six wire names.
//
// These are a published contract: consumers in other services subscribe by
// this string. A typo is not a compile error — it is a silently-dropped
// consumer — so the strings are written out here as literals rather than
// derived from the constants they are testing.
func TestEventNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   domain.Event
		want string
	}{
		{"submitted", Submitted{}, "task.submitted.v1"},
		{"started", Started{}, "task.started.v1"},
		{"succeeded", Succeeded{}, "task.succeeded.v1"},
		{"failed", Failed{}, "task.failed.v1"},
		{"retry_scheduled", RetryScheduled{}, "task.retry_scheduled.v1"},
		{"cancelled", Cancelled{}, "task.cancelled.v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.ev.EventName())
			require.Regexp(t, `^task\.[a-z_]+\.v\d+$`, tc.ev.EventName())
		})
	}
}

// The wire names are global and the Go type names are local, on purpose. The
// wire name keeps the task. prefix because it travels to other services; the Go
// type drops it because tasks.TaskSubmitted is the stutter revive exists to
// prevent. This test fails if someone "fixes" the asymmetry in either
// direction.
func TestEventNamesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, ev := range []domain.Event{
		Submitted{}, Started{}, Succeeded{}, Failed{}, RetryScheduled{}, Cancelled{},
	} {
		name := ev.EventName()
		require.False(t, seen[name], "duplicate wire name %q", name)
		seen[name] = true
	}
	assert.Len(t, seen, 6)
}

// OccurredAt must report the instant the transition happened, which is the At
// field every constructor stamps — not time.Now() read at marshal time. A
// consumer orders on this value.
func TestOccurredAtReportsTheStampedInstant(t *testing.T) {
	at := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	task := pendingTask(t)

	for _, tc := range []struct {
		name string
		ev   domain.Event
	}{
		{"submitted", NewSubmitted(task, at)},
		{"started", NewStarted(task, at)},
		{"succeeded", NewSucceeded(task, nil, at)},
		{"failed", NewFailed(task, "boom", "exploded", at)},
		{"retry_scheduled", NewRetryScheduled(task, at.Add(time.Minute), at)},
		{"cancelled", NewCancelled(task, "user asked", at)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, at.Equal(tc.ev.OccurredAt()))
		})
	}
}

// Every constructor must copy the aggregate's identifying fields onto the
// event. A field that is declared but never populated ships a zero value to
// every consumer — which is what RetryScheduled.Attempt did before it was
// wired up, making retry 1 indistinguishable from retry 5.
func TestEventsCarryTheAggregatesFields(t *testing.T) {
	now := time.Now().UTC()
	task := pendingTask(t)

	t.Run("submitted", func(t *testing.T) {
		ev := NewSubmitted(task, now)
		assert.Equal(t, task.ID(), ev.TaskID)
		assert.Equal(t, task.Handler(), ev.Handler)
		assert.Equal(t, task.Priority(), ev.Priority)
	})

	t.Run("started", func(t *testing.T) {
		ev := NewStarted(task, now)
		assert.Equal(t, task.ID(), ev.TaskID)
		assert.Equal(t, task.Handler(), ev.Handler)
		assert.Equal(t, task.Priority(), ev.Priority)
	})

	t.Run("retry_scheduled carries which attempt failed", func(t *testing.T) {
		running := task
		require.NoError(t, running.Start(uuid.Must(uuid.NewV7()), "w-1", now.Add(time.Minute), now))

		nextAt := now.Add(30 * time.Second)
		ev := NewRetryScheduled(running, nextAt, now)

		assert.Equal(t, uint8(1), ev.Attempt, "must report the attempt that just failed")
		assert.True(t, nextAt.Equal(ev.NextAt))
		assert.Equal(t, running.Priority(), ev.Priority)
	})

	t.Run("failed carries the reason", func(t *testing.T) {
		ev := NewFailed(task, "timeout", "deadline exceeded", now)
		assert.Equal(t, "timeout", ev.ErrorClass)
		assert.Equal(t, "deadline exceeded", ev.ErrorMessage)
	})

	t.Run("cancelled carries the reason", func(t *testing.T) {
		ev := NewCancelled(task, "user asked", now)
		assert.Equal(t, "user asked", ev.Reason)
	})
}
