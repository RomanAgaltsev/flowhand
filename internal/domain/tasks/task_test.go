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

func TestSubmit(t *testing.T) {
	// UUIDv7 to match what the Commander mints, and a UTC instant truncated to
	// timestamptz resolution: time.Now() carries a monotonic reading that
	// reflect.DeepEqual compares as part of the struct, so a bare Now() makes
	// this test pass for reasons that stop holding once a real database is in
	// the loop.
	id := uuid.Must(uuid.NewV7())
	handler := Handler{name: "echo"}
	priority := Priority(1)
	payload := json.RawMessage(`{"msg":"hello"}`)
	maxAttempts := uint8(1)
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	task, err := Submit(id, handler, priority, payload, maxAttempts, createdAt)
	require.NoError(t, err)

	assert.Equal(t, id, task.ID())
	assert.Equal(t, StatusPending, task.Status())
	assert.Equal(t, handler, task.Handler())
	assert.Equal(t, priority, task.Priority())
	assert.Equal(t, maxAttempts, task.MaxAttempts())
	assert.JSONEq(t, `{"msg":"hello"}`, string(task.Payload()))
	// Compare instants, not structs: DeepEqual on time.Time also compares the
	// *Location pointer, so the same instant in two zones would fail.
	assert.True(t, createdAt.Equal(task.CreatedAt()))
	assert.True(t, createdAt.Equal(task.EarliestAt()))

	// A fresh task has no history and exactly one thing to say about itself.
	assert.Empty(t, task.Attempts())
	assert.Zero(t, task.AttemptCount())
	assert.False(t, task.IsTerminal())
	assert.True(t, task.CanRetry())

	events := drain(&task)
	require.Len(t, events, 1)
	assert.Equal(t, "task.submitted.v1", events[0].EventName())
}

// The two invariants Submit owns, because no value object can see them: a
// budget of zero and a nil payload.
func TestSubmitRejects(t *testing.T) {
	now := time.Now().UTC()

	t.Run("zero max attempts", func(t *testing.T) {
		// Zero is not "unlimited": CanRetry would be false forever, so the task
		// could never be retried and never cleanly dead-lettered.
		_, err := Submit(uuid.New(), Handler{name: "echo"}, 0, json.RawMessage(`{}`), 0, now)
		assert.ErrorIs(t, err, ErrInvalidMaxAttempts)
	})

	t.Run("nil payload", func(t *testing.T) {
		// An empty JSON object is legitimate; a nil one is a caller bug.
		_, err := Submit(uuid.New(), Handler{name: "echo"}, 0, nil, 1, now)
		assert.ErrorIs(t, err, ErrNilPayload)
	})

	t.Run("empty JSON object is legitimate", func(t *testing.T) {
		_, err := Submit(uuid.New(), Handler{name: "echo"}, 0, json.RawMessage(`{}`), 1, now)
		assert.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// The transition table.
//
// This is written out separately from the implementation ON PURPOSE. A test
// that derives its legal set from the code it tests proves only that the code
// agrees with itself. These rows are the specification (plan Step 4), duplicated
// deliberately.
//
// Note what is NOT here: there is no running -> failed -> pending retry path.
// A transient failure goes running -> retry_scheduled DIRECTLY, because the
// archive trigger treats `failed` as terminal and would move the row out
// mid-retry (schema.md, 2026-07-04 review F9). `Fail` is terminal.
// ---------------------------------------------------------------------------

const (
	mStart         = "start"
	mComplete      = "complete"
	mFail          = "fail"
	mScheduleRetry = "schedule_retry"
	mCancel        = "cancel"
)

// legalTransitions maps method -> from-status -> resulting status.
var legalTransitions = map[string]map[Status]Status{
	mStart: {
		StatusPending:        StatusRunning,
		StatusRetryScheduled: StatusRunning,
	},
	mComplete: {
		StatusRunning: StatusSucceeded,
	},
	mFail: {
		StatusRunning: StatusFailed,
	},
	mScheduleRetry: {
		StatusRunning: StatusRetryScheduled,
	},
	mCancel: {
		StatusPending:        StatusCanceled,
		StatusRunning:        StatusCanceled,
		StatusRetryScheduled: StatusCanceled,
	},
}

// emittedBy is the event each method must append — exactly one, every time.
var emittedBy = map[string]string{
	mStart:         "task.started.v1",
	mComplete:      "task.succeeded.v1",
	mFail:          "task.failed.v1",
	mScheduleRetry: "task.retry_scheduled.v1",
	mCancel:        "task.cancelled.v1",
}

var allMethods = []string{mStart, mComplete, mFail, mScheduleRetry, mCancel}

var allStatuses = []Status{
	StatusPending, StatusRunning, StatusRetryScheduled,
	StatusSucceeded, StatusFailed, StatusCanceled, StatusDeadLettered,
}

// invoke calls one state-changing method by name.
func invoke(t *Task, method string, now time.Time) error {
	switch method {
	case mStart:
		return t.Start(uuid.Must(uuid.NewV7()), "worker-1", now.Add(time.Minute), now)
	case mComplete:
		return t.Complete(json.RawMessage(`{"ok":true}`), now)
	case mFail:
		return t.Fail("boom", "exploded", now)
	case mScheduleRetry:
		return t.ScheduleRetry("timeout", "deadline exceeded", now.Add(time.Minute), now)
	case mCancel:
		return t.Cancel("user asked", now)
	default:
		panic("unknown method " + method)
	}
}

// taskIn builds an aggregate parked in the given status with budget to spare,
// via FromPersistence so no events are buffered before the method under test
// runs. A running task gets an open attempt, because that is what running
// means.
func taskIn(status Status) Task {
	now := time.Now().UTC().Truncate(time.Microsecond)

	var attempts []Attempt
	switch status {
	case StatusRunning:
		attempts = []Attempt{{
			id: uuid.Must(uuid.NewV7()), status: AttemptRunning,
			workerID: "worker-0", startedAt: now,
		}}
	case StatusRetryScheduled:
		finished := now
		attempts = []Attempt{{
			id: uuid.Must(uuid.NewV7()), status: AttemptFailed,
			workerID: "worker-0", startedAt: now, finishedAt: &finished,
			errorClass: "timeout", errorMessage: "deadline exceeded",
		}}
	}

	return FromPersistence(
		uuid.Must(uuid.NewV7()), status, Handler{name: "echo"}, 1,
		json.RawMessage(`{}`), attempts, 0, now, 5, now,
	)
}

// TestTransitions walks EVERY cell of the status x method grid, not only the
// legal ones. The illegal half is the half that matters: it proves the guard
// runs before the mutation, so a rejected call leaves the aggregate exactly as
// it found it. A guard that returns an error AFTER mutating is worse than no
// guard.
func TestTransitions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)

	for _, method := range allMethods {
		for _, from := range allStatuses {
			want, isLegal := legalTransitions[method][from]

			t.Run(method+"_from_"+string(from), func(t *testing.T) {
				task := taskIn(from)
				err := invoke(&task, method, now)

				if !isLegal {
					require.ErrorIs(t, err, ErrInvalidTransition)
					assert.Equal(t, from, task.Status(), "a rejected call must not mutate")
					assert.Empty(t, drain(&task), "a rejected call must not emit")
					// The message has to name both, or the caller's log line is
					// useless.
					assert.Contains(t, err.Error(), string(from))
					assert.Contains(t, err.Error(), method[:4])
					return
				}

				require.NoError(t, err)
				assert.Equal(t, want, task.Status())

				events := drain(&task)
				require.Len(t, events, 1, "exactly one event per state change")
				assert.Equal(t, emittedBy[method], events[0].EventName())
			})
		}
	}
}

// Start opens an attempt; nothing else does.
func TestStartOpensAnAttempt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := taskIn(StatusPending)
	attemptID := uuid.Must(uuid.NewV7())

	require.NoError(t, task.Start(attemptID, "worker-9", now.Add(time.Minute), now))

	require.Len(t, task.Attempts(), 1)
	a := task.Attempts()[0]
	assert.Equal(t, attemptID, a.ID())
	assert.Equal(t, "worker-9", a.WorkerID())
	assert.Equal(t, AttemptRunning, a.Status())
	assert.True(t, a.IsOpen())
	assert.Equal(t, uint8(1), task.AttemptCount())
}

// Complete and Fail close the open attempt and stamp its outcome.
func TestTerminalMethodsCloseTheOpenAttempt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Run("complete", func(t *testing.T) {
		task := taskIn(StatusRunning)
		require.NoError(t, task.Complete(json.RawMessage(`{}`), now))

		a := task.Attempts()[0]
		assert.Equal(t, AttemptSucceeded, a.Status())
		assert.False(t, a.IsOpen())
		assert.Empty(t, a.ErrorClass())
	})

	t.Run("fail", func(t *testing.T) {
		task := taskIn(StatusRunning)
		require.NoError(t, task.Fail("boom", "exploded", now))

		a := task.Attempts()[0]
		assert.Equal(t, AttemptFailed, a.Status())
		assert.False(t, a.IsOpen())
		assert.Equal(t, "boom", a.ErrorClass())
		assert.Equal(t, "exploded", a.ErrorMessage())
	})
}

// Cancel from retry_scheduled must not overwrite why the last attempt failed:
// ScheduleRetry already closed it, and the cancellation is a fact about the
// TASK, not about that attempt.
func TestCancelDoesNotOverwriteAClosedAttempt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := taskIn(StatusRetryScheduled)

	require.NoError(t, task.Cancel("user asked", now))

	a := task.Attempts()[0]
	assert.Equal(t, "timeout", a.ErrorClass(), "must still say why attempt 1 failed")
	assert.Equal(t, "deadline exceeded", a.ErrorMessage())
}

// ScheduleRetry refuses once the budget is spent, and says so with a DIFFERENT
// sentinel: running -> retry_scheduled is a legal edge, so this is not a caller
// bug. The Commander branches on it to dead-letter instead of retrying.
func TestScheduleRetryRefusesWhenAttemptsExhausted(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)

	task, err := Submit(uuid.Must(uuid.NewV7()), Handler{name: "echo"}, 0,
		json.RawMessage(`{}`), 2, now)
	require.NoError(t, err)

	// Budget of 2: the first retry is legal, the second is not.
	require.NoError(t, task.Start(uuid.New(), "w-1", now.Add(time.Minute), now))
	require.NoError(t, task.ScheduleRetry("timeout", "slow", now.Add(time.Minute), now))
	require.NoError(t, task.Start(uuid.New(), "w-2", now.Add(time.Minute), now))

	err = task.ScheduleRetry("timeout", "slow", now.Add(time.Minute), now)

	require.ErrorIs(t, err, ErrAttemptsExhausted)
	assert.NotErrorIs(t, err, ErrInvalidTransition, "exhaustion is not a caller bug")
	assert.Equal(t, StatusRunning, task.Status(), "a refused retry must not mutate")
	assert.False(t, task.CanRetry())
}

func TestIsTerminal(t *testing.T) {
	for _, status := range allStatuses {
		want := status == StatusSucceeded || status == StatusFailed ||
			status == StatusCanceled || status == StatusDeadLettered
		assert.Equal(t, want, taskIn(status).IsTerminal(), "status %s", status)
	}
}

// CanRetry is exported and the Commander calls it to choose between scheduling
// a retry and dead-lettering. A terminal task has no budget however few
// attempts it used.
func TestCanRetryIsFalseOnceTerminal(t *testing.T) {
	task := taskIn(StatusSucceeded)
	assert.Zero(t, task.AttemptCount())
	assert.Less(t, task.AttemptCount(), task.MaxAttempts(), "budget is nominally left")
	assert.False(t, task.CanRetry(), "but a terminal task can never retry")
}

// ---------------------------------------------------------------------------
// PullEvents
// ---------------------------------------------------------------------------

// The four cases together are what catch the pop-per-yield implementation. The
// early-break case is the one that catches it: a buffer drained element by
// element would still hold the untaken events, and the NEXT PullEvents — in a
// later transaction — would write them to the outbox with the wrong
// transaction's timing.
func TestPullEvents(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)

	newTaskWithThreeEvents := func(t *testing.T) Task {
		t.Helper()
		task, err := Submit(uuid.Must(uuid.NewV7()), Handler{name: "echo"}, 0,
			json.RawMessage(`{}`), 5, now)
		require.NoError(t, err)
		require.NoError(t, task.Start(uuid.New(), "w-1", now.Add(time.Minute), now))
		require.NoError(t, task.Complete(json.RawMessage(`{}`), now))
		return task
	}

	t.Run("full drain yields every event in order", func(t *testing.T) {
		task := newTaskWithThreeEvents(t)

		var got []string
		for ev := range task.PullEvents() {
			got = append(got, ev.EventName())
		}

		assert.Equal(t, []string{
			"task.submitted.v1", "task.started.v1", "task.succeeded.v1",
		}, got)
	})

	t.Run("a second call yields nothing", func(t *testing.T) {
		task := newTaskWithThreeEvents(t)
		require.Len(t, drain(&task), 3)

		assert.Empty(t, drain(&task))
	})

	t.Run("breaking early still leaves the aggregate empty", func(t *testing.T) {
		task := newTaskWithThreeEvents(t)

		var seen int
		for range task.PullEvents() {
			seen++
			break
		}
		require.Equal(t, 1, seen)

		// The buffer was detached at the CALL, not consumed per yield, so the
		// two untaken events are gone rather than waiting to be written by the
		// next transaction.
		assert.Empty(t, drain(&task), "buffer must be empty however the consumer exited")
	})

	t.Run("a state change after a drain buffers exactly one", func(t *testing.T) {
		task, err := Submit(uuid.Must(uuid.NewV7()), Handler{name: "echo"}, 0,
			json.RawMessage(`{}`), 5, now)
		require.NoError(t, err)
		require.Len(t, drain(&task), 1)

		require.NoError(t, task.Start(uuid.New(), "w-1", now.Add(time.Minute), now))

		events := drain(&task)
		require.Len(t, events, 1)
		assert.Equal(t, "task.started.v1", events[0].EventName())
	})
}

// Loading a task from the database is not a domain event — nothing happened.
// A FromPersistence that ran Submit's logic would re-emit task.submitted.v1 on
// every read, and the relay would publish a duplicate submission for every GET.
// Small test, large consequence.
func TestFromPersistenceEmitsNoEvents(t *testing.T) {
	for _, status := range allStatuses {
		t.Run(string(status), func(t *testing.T) {
			task := taskIn(status)
			assert.Empty(t, drain(&task))
		})
	}
}

// The aggregate boundary: nothing outside the package may mutate an attempt.
// Attempts() clones the slice, and the nullable-time getters hand back copies —
// without the second half, a caller could write through the returned pointer
// and reopen a closed attempt from outside.
func TestAttemptsCannotBeMutatedFromOutside(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := taskIn(StatusRunning)
	require.NoError(t, task.Complete(json.RawMessage(`{}`), now))

	// Writing into the returned slice must not reach the aggregate.
	stolen := task.Attempts()
	stolen[0].workerID = "attacker"
	assert.Equal(t, "worker-0", task.Attempts()[0].WorkerID())

	// Nor may writing through the returned pointer.
	finished := task.Attempts()[0].FinishedAt()
	require.NotNil(t, finished)
	*finished = finished.Add(24 * time.Hour)
	assert.True(t, now.Equal(*task.Attempts()[0].FinishedAt()))
}

// drain collects a full PullEvents into a slice.
func drain(t *Task) []domain.Event {
	var events []domain.Event
	for ev := range t.PullEvents() {
		events = append(events, ev)
	}
	return events
}

// pendingTask is a freshly-submitted task with its Submitted event already
// drained, so a test starts from an empty buffer.
func pendingTask(t *testing.T) Task {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task, err := Submit(uuid.Must(uuid.NewV7()), Handler{name: "echo"}, 7,
		json.RawMessage(`{}`), 5, now)
	require.NoError(t, err)
	drain(&task)
	return task
}
