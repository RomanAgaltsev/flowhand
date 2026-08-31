package tasks

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/stretchr/testify/require"
)

// propMaxAttempts is the budget every generated task starts with. Three is the
// smallest number that lets a sequence exhaust the budget without needing a
// long generated slice to get there.
const propMaxAttempts uint8 = 3

// model is an independent reimplementation of the Step 4 transition table —
// deliberately NOT the aggregate. If the property derived its expectations from
// Task's own guards it would prove only that the code agrees with itself.
type model struct {
	status   Status
	attempts uint8
}

// allows reports whether the table permits method from the model's current
// state, exactly as the plan's table specifies it.
func (m model) allows(method string) bool {
	if _, ok := legalTransitions[method][m.status]; !ok {
		return false
	}
	// The one extra rule the table carries in prose: ScheduleRetry refuses once
	// the budget is spent. It is a different failure from an illegal edge.
	if method == mScheduleRetry && m.attempts >= propMaxAttempts {
		return false
	}
	return true
}

// apply advances the model. Only Start consumes budget.
func (m model) apply(method string) model {
	m.status = legalTransitions[method][m.status]
	if method == mStart {
		m.attempts++
	}
	return m
}

// methodSeq generates sequences of method names, legal and illegal mixed.
func methodSeq() gopter.Gen {
	return gen.SliceOf(gen.OneConstOf(
		mStart, mComplete, mFail, mScheduleRetry, mCancel,
	), reflect.TypeOf(""))
}

func propParameters() *gopter.Properties {
	params := gopter.DefaultTestParameters()
	params.MinSuccessfulTests = 10_000
	return gopter.NewProperties(params)
}

// applyAll runs a whole generated sequence, letting refusals pass. Properties
// 3 and 4 are about where the aggregate ENDS UP after arbitrary traffic, not
// about which individual calls were legal — property 1 is what checks those.
func applyAll(task *Task, methods []string, now time.Time) {
	for _, method := range methods {
		if err := invoke(task, method, now); err != nil {
			continue
		}
	}
}

// newPropTask is a freshly-submitted task with its Submitted event drained, so
// each property starts from an empty buffer and a known model state.
func newPropTask(t *testing.T, now time.Time) Task {
	t.Helper()
	task, err := Submit(uuid.Must(uuid.NewV7()), Handler{name: "echo"}, 0,
		json.RawMessage(`{}`), propMaxAttempts, now)
	require.NoError(t, err)
	drain(&task)
	return task
}

// TestAggregateProperties runs the four invariants over sequences no one would
// have written by hand.
func TestAggregateProperties(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	props := propParameters()

	// 1. Reachability. Every sequence the table calls legal must succeed and
	//    land where the table says; every illegal one must error and leave the
	//    status untouched.
	props.Property("the aggregate accepts exactly the table's transitions", prop.ForAll(
		func(methods []string) bool {
			task := newPropTask(t, now)
			m := model{status: StatusPending}

			for _, method := range methods {
				before := task.Status()
				err := invoke(&task, method, now)
				drain(&task)

				if m.allows(method) {
					if err != nil || task.Status() != m.apply(method).status {
						return false
					}
					m = m.apply(method)
					continue
				}

				// Illegal: must error AND must not have mutated.
				if err == nil || task.Status() != before {
					return false
				}
				// The two refusal reasons are distinguishable, and callers
				// branch on the difference.
				if method == mScheduleRetry && before == StatusRunning {
					if !errors.Is(err, ErrAttemptsExhausted) {
						return false
					}
				} else if !errors.Is(err, ErrInvalidTransition) {
					return false
				}
			}
			return task.Status() == m.status
		},
		methodSeq(),
	))

	// 2. Event/attempt parity. Start and Started cannot drift apart: an
	//    aggregate that opened an attempt without announcing it (or vice versa)
	//    leaves the outbox disagreeing with task_attempts.
	props.Property("started events equal the attempt count", prop.ForAll(
		func(methods []string) bool {
			task := newPropTask(t, now)

			var started int
			for _, method := range methods {
				if err := invoke(&task, method, now); err != nil {
					continue
				}
				for _, ev := range drain(&task) {
					if ev.EventName() == "task.started.v1" {
						started++
					}
				}
			}
			return started == int(task.AttemptCount())
		},
		methodSeq(),
	))

	// 3. Cancel idempotence. A repeated terminal call is a clean refusal, not a
	//    second event — otherwise every duplicate cancellation request would
	//    publish another task.cancelled.v1.
	props.Property("cancelling a cancelled task is a no-op", prop.ForAll(
		func(methods []string) bool {
			task := newPropTask(t, now)
			applyAll(&task, methods, now)
			drain(&task)

			if err := task.Cancel("first", now); err != nil {
				// Already terminal (or never live): still must not emit.
				return len(drain(&task)) == 0
			}
			// The first cancel landed; the second must be refused and silent.
			if len(drain(&task)) != 1 {
				return false
			}
			err := task.Cancel("second", now)
			return errors.Is(err, ErrInvalidTransition) &&
				len(drain(&task)) == 0 &&
				task.Status() == StatusCanceled
		},
		methodSeq(),
	))

	// 4. PullEvents exhaustion. However many events a sequence buffered, one
	//    full drain empties the aggregate — the property the Commander relies on
	//    to call it once per transaction without double-writing.
	props.Property("a second drain always yields nothing", prop.ForAll(
		func(methods []string) bool {
			task := newPropTask(t, now)
			applyAll(&task, methods, now)

			drain(&task)
			return len(drain(&task)) == 0
		},
		methodSeq(),
	))

	props.TestingRun(t)
}
