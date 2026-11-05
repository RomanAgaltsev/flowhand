//go:build integration

package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/repository/outbox"
	repotasks "github.com/RomanAgaltsev/flowhand/internal/repository/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/storage/txmgr"
	"github.com/RomanAgaltsev/flowhand/internal/testutil/pgtest"
)

// fakeCatalog stands in for the worker registry on the write path, the same
// role it plays in mapper_test.go. Re-declared here because that file is in
// the internal test package and this one is external.
type fakeCatalog struct{}

func (fakeCatalog) Has(string) bool { return true }

// submitted builds a pending task the way the Commander does: through the
// validating constructors, with the service layer's defaults for priority and
// max_attempts.
func submitted(t *testing.T, payload json.RawMessage) domaintasks.Task {
	t.Helper()
	handler, err := domaintasks.NewHandler("echo", fakeCatalog{})
	require.NoError(t, err)
	task, err := domaintasks.Submit(
		uuid.Must(uuid.NewV7()), handler, 0, payload, 25,
		time.Now().UTC().Truncate(time.Microsecond),
	)
	require.NoError(t, err)
	return task
}

// claimFor builds the claim the Commander would, for a key and a payload.
func claimFor(t *testing.T, rawKey, payload string) *domaintasks.IdempotencyClaim {
	t.Helper()
	key, err := domaintasks.NewIdempotencyKey(rawKey)
	require.NoError(t, err)
	fp, err := domaintasks.NewPayloadFingerprint(json.RawMessage(payload))
	require.NoError(t, err)
	return &domaintasks.IdempotencyClaim{Key: key, Fingerprint: fp}
}

// TestRepo_InsertThenGet_PersistsHandler closes a gap: every unit test in this
// package would still pass if `handler` were
// dropped between the mapper and the database, because none of them touch a
// database. This one does.
func TestRepo_InsertThenGet_PersistsHandler(t *testing.T) {
	pool := pgtest.Postgres(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := t.Context()

	want := submitted(t, json.RawMessage(`{"msg":"hi"}`))
	require.NoError(t, repo.Insert(ctx, want, nil))

	got, err := repo.Get(ctx, want.ID())
	require.NoError(t, err)

	assert.Equal(t, want.ID(), got.ID())
	// The assertion no unit test can make: the handler name survives the
	// round-trip. Compared as a value object, rebuilt through the
	// same read-side constructor the mapper uses.
	assert.Equal(t, domaintasks.HandlerFromPersistence("echo"), got.Handler())
	assert.Equal(t, domaintasks.StatusPending, got.Status(), "status comes from the aggregate")
	assert.JSONEq(t, `{"msg":"hi"}`, string(got.Payload()))
	assert.True(t, want.CreatedAt().Equal(got.CreatedAt()),
		"the row must carry the instant the submit answered with: wrote %s, read %s",
		want.CreatedAt(), got.CreatedAt())
}

// TestRepo_DuplicateKeyIsErrConflict proves the link the Commander's fakes
// cannot: that a real Postgres 23505 actually becomes domaintasks.ErrConflict.
// Delete the SQLSTATE check in postgres.go and every unit test still passes
// while this one fails.
//
// The 23505 comes from idempotency_keys' composite primary key, not
// from a unique index on `tasks` — that index was dropped because the archive
// trigger would evaporate it the moment a task completed.
func TestRepo_DuplicateKeyIsErrConflict(t *testing.T) {
	pool := pgtest.Postgres(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := t.Context()
	claim := claimFor(t, "k-1", `{}`)

	newTask := func() domaintasks.Task { return submitted(t, json.RawMessage(`{}`)) }

	require.NoError(t, repo.Insert(ctx, newTask(), claim))
	require.ErrorIs(t, repo.Insert(ctx, newTask(), claim), domaintasks.ErrConflict)

	// A keyless submit writes no ledger row at all, so keyless submits must never
	// collide.
	require.NoError(t, repo.Insert(ctx, newTask(), nil))
	require.NoError(t, repo.Insert(ctx, newTask(), nil))
}

// TestRepo_GetMissingIsErrNotFound pins the other half of translate().
func TestRepo_GetMissingIsErrNotFound(t *testing.T) {
	pool := pgtest.Postgres(t)
	repo := repotasks.New(txmgr.New(pool))

	_, err := repo.Get(t.Context(), uuid.Must(uuid.NewV7()))
	require.ErrorIs(t, err, domaintasks.ErrNotFound)

	echo := domaintasks.HandlerFromPersistence("echo")
	key, err := domaintasks.NewIdempotencyKey("never-used")
	require.NoError(t, err)
	_, err = repo.GetIdempotencyRecord(t.Context(), echo, key)
	require.ErrorIs(t, err, domaintasks.ErrNotFound)
}

// TestRepo_PersistTransition_StaleEpochReturnsZero: simulate a claim (stamp
// lease_epoch=1, status=running), transition the aggregate, then fence on a
// stale epoch 99 — the epoch a reclaim would have bumped past us.
func TestRepo_PersistTransition_StaleEpochReturnsZero(t *testing.T) {
	pool := pgtest.Postgres(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := t.Context()

	tt := submitted(t, json.RawMessage(`{}`))
	require.NoError(t, repo.Insert(ctx, tt, nil))
	_, err := pool.Exec(ctx,
		`UPDATE tasks SET status='running', worker_id='w1', started_at=now(), lease_epoch=1 WHERE id=$1`, tt.ID())
	require.NoError(t, err)

	got, err := repo.GetForUpdate(ctx, tt.ID())
	require.NoError(t, err)
	require.NoError(t, got.ScheduleRetry("timeout", "boom",
		time.Now().Add(time.Minute), time.Now().UTC()))

	rows, err := repo.PersistTransition(ctx, got, 99) // stale fence
	require.NoError(t, err)
	assert.Zero(t, rows, "stale epoch must fence the write out")

	after, err := repo.Get(ctx, tt.ID())
	require.NoError(t, err)
	assert.Equal(t, domaintasks.StatusRunning, after.Status(), "row must be untouched")
}

// poisonEvent fails json.Marshal honestly: an exported channel field. This
// exercises the REAL error path in encodeEnvelope — proving the composition
// rolls back, not that a mock returned an error.
type poisonEvent struct {
	TaskID uuid.UUID
	Ch     chan struct{} // exported: encoding/json refuses channels
	At     time.Time
}

func (poisonEvent) EventName() string        { return "task.poison.v1" }
func (e poisonEvent) OccurredAt() time.Time  { return e.At }
func (e poisonEvent) AggregateID() uuid.UUID { return e.TaskID }

func TestInsertAndAppendAreAtomic(t *testing.T) {
	pool := pgtest.Postgres(t)
	txm := txmgr.New(pool)
	tasksRepo := repotasks.New(txm)
	outboxRepo := outbox.New(txm)
	ctx := t.Context()

	t.Run("commit makes both visible", func(t *testing.T) {
		tt := submitted(t, json.RawMessage(`{"msg":"hi"}`))
		require.NoError(t, txm.WithinTx(ctx, func(ctx context.Context) error {
			if err := tasksRepo.Insert(ctx, tt, nil); err != nil {
				return err
			}
			return outboxRepo.Append(ctx, domaintasks.NewSubmitted(tt, time.Now().UTC()))
		}))

		_, err := tasksRepo.Get(ctx, tt.ID())
		require.NoError(t, err)
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1`, tt.ID()).Scan(&n))
		assert.Equal(t, 1, n)
	})

	t.Run("append failure rolls back the task row", func(t *testing.T) {
		tt := submitted(t, json.RawMessage(`{}`))
		err := txm.WithinTx(ctx, func(ctx context.Context) error {
			if err := tasksRepo.Insert(ctx, tt, nil); err != nil {
				return err
			}
			return outboxRepo.Append(ctx,
				domaintasks.NewSubmitted(tt, time.Now().UTC()),
				poisonEvent{TaskID: tt.ID(), At: time.Now().UTC()},
			)
		})
		require.ErrorContains(t, err, "encode task.poison.v1")

		_, gerr := tasksRepo.Get(ctx, tt.ID())
		require.ErrorIs(t, gerr, domaintasks.ErrNotFound, "task row must not exist")
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1`, tt.ID()).Scan(&n))
		assert.Zero(t, n)
	})

	// The poison case above fails BEFORE any outbox SQL runs, so it cannot tell
	// resolver-bound and self-transacting implementations apart. This one fails
	// AFTER the batch has landed: if Append resolved the ambient transaction,
	// its rows die with the outer rollback; if it opened its own transaction,
	// they survive it.
	t.Run("failure after append rolls both back", func(t *testing.T) {
		tt := submitted(t, json.RawMessage(`{}`))
		err := txm.WithinTx(ctx, func(ctx context.Context) error {
			if err := tasksRepo.Insert(ctx, tt, nil); err != nil {
				return err
			}
			if err := outboxRepo.Append(ctx, domaintasks.NewSubmitted(tt, time.Now().UTC())); err != nil {
				return err
			}
			return errors.New("boom after append")
		})
		require.ErrorContains(t, err, "boom after append")

		_, gerr := tasksRepo.Get(ctx, tt.ID())
		require.ErrorIs(t, gerr, domaintasks.ErrNotFound, "task row must not exist")
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM outbox_events WHERE aggregate_id=$1`, tt.ID()).Scan(&n))
		assert.Zero(t, n, "outbox rows must roll back with the outer transaction")
	})
}

// TestRepo_GetFallsBackToArchive pins the read rule: a terminal task
// leaves `tasks` the moment it finishes, and Get must still find it. The direct
// UPDATE stands in for the scheduler - the trigger does not care who wrote it.
func TestRepo_GetFallsBackToArchive(t *testing.T) {
	pool := pgtest.Postgres(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := t.Context()

	tt := submitted(t, json.RawMessage(`{"msg":"done"}`))
	require.NoError(t, repo.Insert(ctx, tt, nil))
	_, err := pool.Exec(ctx, `UPDATE tasks SET status = 'succeeded' WHERE id = $1`, tt.ID())
	require.NoError(t, err)

	// The precondition, asserted rather than assumed: the trigger really moved it.
	var hot, archived int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE id = $1`, tt.ID()).Scan(&hot))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM tasks_archive WHERE id = $1`, tt.ID()).Scan(&archived))
	require.Equal(t, 0, hot, "the archive trigger must have removed the hot row")
	require.Equal(t, 1, archived, "the archive trigger must have written the archive row")

	// The default partition is a safety net that has to stay empty: a row in it
	// blocks creating the partition its month belongs to.
	var unpartitioned int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM tasks_archive_default`).Scan(&unpartitioned))
	require.Zero(t, unpartitioned, "a finished task must land in its month's partition")

	got, err := repo.Get(ctx, tt.ID())
	require.NoError(t, err, "a finished task is archived, not gone")
	assert.Equal(t, tt.ID(), got.ID())
	assert.Equal(t, domaintasks.StatusSucceeded, got.Status())
	assert.Equal(t, domaintasks.HandlerFromPersistence("echo"), got.Handler())
	assert.JSONEq(t, `{"msg":"done"}`, string(got.Payload()))

	// GetForUpdate stays hot-only: an archived task has no transition left to
	// write, so a write path that found one would be a bug, not a feature.
	_, err = repo.GetForUpdate(ctx, tt.ID())
	require.ErrorIs(t, err, domaintasks.ErrNotFound)
}

// TestRepo_GetIdempotencyRecord pins the ledger read the replay depends on:
// it returns what Insert wrote, it is scoped by handler exactly as the primary
// key is, and it still answers after the task row has moved to the archive.
func TestRepo_GetIdempotencyRecord(t *testing.T) {
	pool := pgtest.Postgres(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := t.Context()

	tt := submitted(t, json.RawMessage(`{"b":2,"a":1}`))
	claim := claimFor(t, "k-1", `{"b":2,"a":1}`)
	require.NoError(t, repo.Insert(ctx, tt, claim))
	key := claim.Key
	wantFP, err := domaintasks.NewPayloadFingerprint(json.RawMessage(`{"a":1,"b":2}`))
	require.NoError(t, err)

	rec, err := repo.GetIdempotencyRecord(ctx, tt.Handler(), key)
	require.NoError(t, err)
	assert.Equal(t, tt.ID(), rec.TaskID)
	assert.Equal(t, wantFP, rec.Fingerprint,
		"the ledger must hold the CANONICAL fingerprint - the reordered payload hashes the same")

	// The same key under another handler is another ledger row - here, none.
	_, err = repo.GetIdempotencyRecord(ctx, domaintasks.HandlerFromPersistence("other"), key)
	require.ErrorIs(t, err, domaintasks.ErrNotFound)

	// The ledger outlives the hot row: archive the task and read again.
	_, err = pool.Exec(ctx, `UPDATE tasks SET status = 'succeeded' WHERE id = $1`, tt.ID())
	require.NoError(t, err)
	rec, err = repo.GetIdempotencyRecord(ctx, tt.Handler(), key)
	require.NoError(t, err, "the ledger row must survive the task's archival")
	assert.Equal(t, tt.ID(), rec.TaskID)
}

// TestRepo_GetIdempotencyRecord_RefusesAFingerprintOfTheWrongWidth is the bullet of
// the method's contract that TestFingerprintFromRow proves only in isolation. A bare
// conversion panics below 16 bytes, and above it silently keeps a prefix, which
// compares unequal and answers a genuine replay with a 409.
func TestRepo_GetIdempotencyRecord_RefusesAFingerprintOfTheWrongWidth(t *testing.T) {
	pool := pgtest.Postgres(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := t.Context()

	tt := submitted(t, json.RawMessage(`{}`))
	claim := claimFor(t, "k-1", `{}`)
	require.NoError(t, repo.Insert(ctx, tt, claim))

	for _, width := range []int{8, 32} {
		_, err := pool.Exec(ctx, `UPDATE idempotency_keys SET payload_hash = $1`, make([]byte, width))
		require.NoError(t, err)

		_, err = repo.GetIdempotencyRecord(ctx, tt.Handler(), claim.Key)
		require.ErrorIs(t, err, domaintasks.ErrInvalidFingerprint, "%d bytes", width)
		assert.ErrorContains(t, err, "k-1", "a corrupt row must be findable from the log line")
	}
}

// TestRepo_KeylessInsertIsNeverErrConflict pins the invariant where it lives. Only
// the ledger insert maps a unique violation to ErrConflict, which is why the
// Commander can treat ErrConflict as "replay" without asking where it came from.
func TestRepo_KeylessInsertIsNeverErrConflict(t *testing.T) {
	repo := repotasks.New(txmgr.New(pgtest.Postgres(t)))
	ctx := t.Context()

	tt := submitted(t, json.RawMessage(`{}`))
	require.NoError(t, repo.Insert(ctx, tt, nil))

	err := repo.Insert(ctx, tt, nil) // the same id: a primary-key collision on tasks
	require.Error(t, err)
	assert.NotErrorIs(t, err, domaintasks.ErrConflict)
}

// TestRepo_Insert_StampsTheTraceID pins the column the outbox row already fills: a
// task and the event it emitted have to be joinable on the same trace.
func TestRepo_Insert_StampsTheTraceID(t *testing.T) {
	pool := pgtest.Postgres(t)
	repo := repotasks.New(txmgr.New(pool))

	traced := submitted(t, json.RawMessage(`{}`))
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	require.NoError(t, repo.Insert(ctx, traced, nil))

	untraced := submitted(t, json.RawMessage(`{}`))
	require.NoError(t, repo.Insert(t.Context(), untraced, nil))

	var got *string
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT trace_id FROM tasks WHERE id = $1`, traced.ID()).Scan(&got))
	require.NotNil(t, got)
	assert.Equal(t, traceID.String(), *got)

	require.NoError(t, pool.QueryRow(t.Context(), `SELECT trace_id FROM tasks WHERE id = $1`, untraced.ID()).Scan(&got))
	assert.Nil(t, got)
}

// TestRepo_PersistTransition_LeavesErrorStackAlone pins what a closing write must
// not touch. Nothing in the domain carries a stack yet, so a close that wrote the
// column would erase whatever the row holds with a NULL.
func TestRepo_PersistTransition_LeavesErrorStackAlone(t *testing.T) {
	pool := pgtest.Postgres(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := t.Context()

	tt := submitted(t, json.RawMessage(`{}`))
	require.NoError(t, repo.Insert(ctx, tt, nil))
	_, err := pool.Exec(ctx, `UPDATE tasks SET lease_epoch = 1 WHERE id = $1`, tt.ID())
	require.NoError(t, err)

	// Dispatch opens the attempt.
	now := time.Now().UTC()
	got, err := repo.GetForUpdate(ctx, tt.ID())
	require.NoError(t, err)
	require.NoError(t, got.Start(uuid.Must(uuid.NewV7()), "w1", now.Add(time.Minute), now))
	rows, err := repo.PersistTransition(ctx, got, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	_, err = pool.Exec(ctx, `UPDATE task_attempts SET error_stack = 'kept' WHERE task_id = $1`, tt.ID())
	require.NoError(t, err)

	// The result closes it.
	got, err = repo.GetForUpdate(ctx, tt.ID())
	require.NoError(t, err)
	require.NoError(t, got.ScheduleRetry("timeout", "boom", now.Add(time.Minute), now))
	rows, err = repo.PersistTransition(ctx, got, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	var class, stack *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT error_class, error_stack FROM task_attempts WHERE task_id = $1`, tt.ID()).Scan(&class, &stack))
	require.NotNil(t, class)
	assert.Equal(t, "timeout", *class, "the close must still write what it owns")
	require.NotNil(t, stack, "the close must not erase error_stack")
	assert.Equal(t, "kept", *stack)
}

// TestRepo_Get_NewestArchiveRowWins pins the read order. (id, finished_at) is the
// archive's key, so one id can hold two rows, and an unordered read would return
// whichever a partition scan reached first.
func TestRepo_Get_NewestArchiveRowWins(t *testing.T) {
	pool := pgtest.Postgres(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := t.Context()

	tt := submitted(t, json.RawMessage(`{}`))
	require.NoError(t, repo.Insert(ctx, tt, nil))
	_, err := pool.Exec(ctx, `UPDATE tasks SET status = 'succeeded' WHERE id = $1`, tt.ID())
	require.NoError(t, err)

	// One statement, because a temp table belongs to one pooled connection. No
	// arguments, because a multi-statement Exec has to use the simple protocol.
	_, err = pool.Exec(ctx, `
		CREATE TEMP TABLE older AS SELECT * FROM tasks_archive WHERE id = '`+tt.ID().String()+`';
		UPDATE older SET status = 'failed', finished_at = finished_at - interval '1 hour';
		INSERT INTO tasks_archive SELECT * FROM older;
		DROP TABLE older;`)
	require.NoError(t, err)

	got, err := repo.Get(ctx, tt.ID())
	require.NoError(t, err)
	assert.Equal(t, domaintasks.StatusSucceeded, got.Status())
}

// TestArchivePartitions_CoverNextMonth is the alarm for the partition runway. Nothing
// provisions partitions yet: migrations do it by hand, so a month nobody provided for
// sends every finished task into tasks_archive_default. This fails while there is
// still a month to add the next migration.
func TestArchivePartitions_CoverNextMonth(t *testing.T) {
	pool := pgtest.Postgres(t)

	for months, name := range []string{"this month", "next month"} {
		var exists bool
		require.NoError(t, pool.QueryRow(t.Context(), `
			SELECT to_regclass('tasks_archive_' ||
				to_char(now() + make_interval(months => $1), 'YYYY_MM')) IS NOT NULL`,
			months).Scan(&exists))
		assert.True(t, exists, "%s has no tasks_archive partition: add a migration", name)
	}
}
