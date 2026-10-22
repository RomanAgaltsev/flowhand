package tasks

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
)

// goldenAt, goldenID and goldenTask are fixed so the payload tests below can
// compare exact bytes. Nothing here may use time.Now or a random UUID.
var (
	goldenAt = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	goldenID = uuid.MustParse("018f1e60-0000-7000-8000-000000000001")
)

func goldenTask(t *testing.T) Task {
	t.Helper()
	task, err := Submit(goldenID, Handler{name: "echo"}, 3, json.RawMessage(`{}`), 5, goldenAt)
	require.NoError(t, err)
	drain(&task)
	return task
}

// TestEventNames pins the exact six wire names.
//
// These are a published contract: consumers in other services subscribe by
// this string. A typo is not a compile error - it is a silently-dropped
// consumer - so the strings are written out here as literals rather than
// derived from the constants they are testing.
func TestEventNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind Kind
		want string
	}{
		{"submitted", KindSubmitted, "task.submitted.v1"},
		{"started", KindStarted, "task.started.v1"},
		{"succeeded", KindSucceeded, "task.succeeded.v1"},
		{"failed", KindFailed, "task.failed.v1"},
		{"retry_scheduled", KindRetryScheduled, "task.retry_scheduled.v1"},
		{"cancelled", KindCancelled, "task.cancelled.v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, Event{kind: tc.kind}.EventName())
			require.Regexp(t, `^task\.[a-z_]+\.v\d+$`, Event{kind: tc.kind}.EventName())
		})
	}
}

// TestEveryKindHasAName walks the whole Kind range rather than a hand-written
// list, so a seventh Kind added without a name in EventName's switch fails here
// even if someone bypasses the exhaustive linter. This is the test the six
// independent event types could not have.
func TestEveryKindHasAName(t *testing.T) {
	seen := map[string]bool{}
	for k := KindSubmitted; k <= KindCancelled; k++ {
		name := Event{kind: k}.EventName()
		require.NotEmpty(t, name, "Kind %d has no wire name", k)
		require.False(t, seen[name], "duplicate wire name %q", name)
		seen[name] = true
	}
	assert.Len(t, seen, 6)
}

// The zero Kind is invalid, and both the name and the payload must say so
// rather than silently publishing a nameless event.
func TestZeroKindIsRejected(t *testing.T) {
	assert.Empty(t, Event{}.EventName())

	_, err := json.Marshal(Event{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown kind 0")
}

// OccurredAt must report the instant the transition happened, which is the at
// field every constructor stamps - not time.Now() read at marshal time. A
// consumer orders on this value.
func TestOccurredAtReportsTheStampedInstant(t *testing.T) {
	task := goldenTask(t)

	for _, tc := range []struct {
		name string
		ev   domain.Event
	}{
		{"submitted", NewSubmitted(task, goldenAt)},
		{"started", NewStarted(task, goldenAt)},
		{"succeeded", NewSucceeded(task, nil, goldenAt)},
		{"failed", NewFailed(task, "boom", "exploded", goldenAt)},
		{"retry_scheduled", NewRetryScheduled(task, goldenAt.Add(time.Minute), goldenAt)},
		{"cancelled", NewCancelled(task, "user asked", goldenAt)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, goldenAt.Equal(tc.ev.OccurredAt()))
			assert.Equal(t, task.ID(), tc.ev.AggregateID())
		})
	}
}

// Every constructor must copy the aggregate's identifying fields onto the
// event. A field that is declared but never populated ships a zero value to
// every consumer - which is what RetryScheduled.Attempt did before it was
// wired up, making retry 1 indistinguishable from retry 5.
func TestEventsCarryTheAggregatesFields(t *testing.T) {
	task := goldenTask(t)

	t.Run("submitted", func(t *testing.T) {
		ev := NewSubmitted(task, goldenAt)
		assert.Equal(t, task.ID(), ev.taskID)
		assert.Equal(t, task.Handler(), ev.handler)
		assert.Equal(t, task.Priority(), ev.priority)
	})

	t.Run("started", func(t *testing.T) {
		ev := NewStarted(task, goldenAt)
		assert.Equal(t, task.ID(), ev.taskID)
		assert.Equal(t, task.Handler(), ev.handler)
		assert.Equal(t, task.Priority(), ev.priority)
	})

	t.Run("retry_scheduled carries which attempt failed", func(t *testing.T) {
		running := task
		require.NoError(t, running.Start(uuid.Must(uuid.NewV7()), "w-1", goldenAt.Add(time.Minute), goldenAt))

		nextAt := goldenAt.Add(30 * time.Second)
		ev := NewRetryScheduled(running, nextAt, goldenAt)

		assert.Equal(t, uint8(1), ev.attempt, "must report the attempt that just failed")
		assert.True(t, nextAt.Equal(ev.nextAt))
		assert.Equal(t, running.Priority(), ev.priority)
	})

	t.Run("failed carries the reason", func(t *testing.T) {
		ev := NewFailed(task, "timeout", "deadline exceeded", goldenAt)
		assert.Equal(t, "timeout", ev.errorClass)
		assert.Equal(t, "deadline exceeded", ev.errorMessage)
	})

	t.Run("cancelled carries the reason", func(t *testing.T) {
		ev := NewCancelled(task, "user asked", goldenAt)
		assert.Equal(t, "user asked", ev.reason)
	})
}

// TestPayloadGoldens pins the exact payload bytes of all six kinds.
//
// Two things are being proven, and both need BYTE equality rather than
// assert.JSONEq. First, the keys are snake_case, matching the envelope that
// wraps them. Second - the reason the collapse is safe - each payload carries
// ONLY the fields its kind uses: a submitted event has no "reason", a cancelled
// event has no "result". That is the property the six separate structs used to
// get from the compiler, and this is where it now lives.
//
// These goldens are also what makes the Go 1.27 encoding/json/v2 switch
// provable rather than assumed.
func TestPayloadGoldens(t *testing.T) {
	task := goldenTask(t)
	running := task
	require.NoError(t, running.Start(uuid.Must(uuid.NewV7()), "w-1", goldenAt.Add(time.Minute), goldenAt))

	const head = `{"task_id":"018f1e60-0000-7000-8000-000000000001",` +
		`"handler":"echo","priority":3,"at":"2026-09-18T12:00:00Z"`

	for _, tc := range []struct {
		name string
		ev   Event
		want string
	}{
		{"submitted", NewSubmitted(task, goldenAt), head + `}`},
		{"started", NewStarted(task, goldenAt), head + `}`},
		{
			"succeeded", NewSucceeded(task, json.RawMessage(`{"ok":true}`), goldenAt),
			head + `,"result":{"ok":true}}`,
		},
		{
			"failed", NewFailed(task, "timeout", "dep took too long", goldenAt),
			head + `,"error_class":"timeout","error_message":"dep took too long"}`,
		},
		{
			"retry_scheduled", NewRetryScheduled(running, goldenAt.Add(30*time.Second), goldenAt),
			head + `,"attempt":1,"next_at":"2026-09-18T12:00:30Z"}`,
		},
		{
			"cancelled", NewCancelled(task, "user asked", goldenAt),
			head + `,"reason":"user asked"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.ev)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}
}
