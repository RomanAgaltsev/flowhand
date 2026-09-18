//go:build integration

package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/repository/outbox"
	repotasks "github.com/RomanAgaltsev/flowhand/internal/repository/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/storage"
	"github.com/RomanAgaltsev/flowhand/internal/storage/txmgr"
	"github.com/RomanAgaltsev/flowhand/migrations"
)

// startPostgresWithMigrations brings up a throwaway Postgres, applies every
// goose migration against it, and returns a pool pointed at it.
//
// Migrations run through the goose *library* rather than the CLI so the test is
// self-contained: what CI exercises is the same migration set `task migrate:up`
// applies, with no external binary on PATH.
func startPostgresWithMigrations(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pg, err := postgres.Run(
		ctx, "postgres:18-alpine",
		postgres.WithDatabase("flowhand"),
		postgres.WithUsername("flowhand"),
		postgres.WithPassword("flowhand"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, pg)

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := storage.NewPool(ctx, config.DB{DSN: dsn, MaxConns: 4, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// Migrate through the very provider `flowhand migrate up` uses, rather than a
	// second copy of its locker config that would drift from it. Its pool is
	// transient — closed as soon as the migrations land — while the pool returned
	// above is the one the repository under test runs on.
	p, closeProvider, err := storage.NewGooseProvider(
		ctx,
		config.DB{DSN: dsn, MaxConns: 2, MinConns: 1},
		migrations.FS,
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, closeProvider()) }()

	_, err = p.Up(ctx)
	require.NoError(t, err, "migrations must apply cleanly")

	return pool
}

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

// TestRepo_InsertThenGet_PersistsHandler closes the gap Task L2's acceptance
// named: every unit test in this package would still pass if `handler` were
// dropped between the mapper and the database, because none of them touch a
// database. This one does.
func TestRepo_InsertThenGet_PersistsHandler(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := context.Background()

	want := submitted(t, json.RawMessage(`{"msg":"hi"}`))
	require.NoError(t, repo.Insert(ctx, want, nil))

	got, err := repo.Get(ctx, want.ID())
	require.NoError(t, err)

	assert.Equal(t, want.ID(), got.ID())
	// The assertion no unit test can make: the handler name survives the
	// round-trip. Compared as the value object D2 gave it, rebuilt through the
	// same read-side constructor the mapper uses.
	assert.Equal(t, domaintasks.HandlerFromPersistence("echo"), got.Handler())
	assert.Equal(t, domaintasks.StatusPending, got.Status(), "status comes from the 00001 default")
	assert.JSONEq(t, `{"msg":"hi"}`, string(got.Payload()))
	assert.False(t, got.CreatedAt().IsZero(), "created_at comes from the 00001 default")
}

// TestRepo_DuplicateKeyIsErrConflict proves the link the Commander's fakes
// cannot: that a real Postgres 23505 actually becomes domaintasks.ErrConflict.
// Delete the SQLSTATE check in postgres.go and every unit test still passes
// while this one fails.
//
// Since S0 the 23505 comes from idempotency_keys' composite primary key, not
// from a unique index on `tasks` — that index was dropped because the archive
// trigger would evaporate it the moment a task completed (ADR 0005).
func TestRepo_DuplicateKeyIsErrConflict(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := context.Background()
	key := "k-1"

	newTask := func() domaintasks.Task { return submitted(t, json.RawMessage(`{}`)) }

	require.NoError(t, repo.Insert(ctx, newTask(), &key))
	require.ErrorIs(t, repo.Insert(ctx, newTask(), &key), domaintasks.ErrConflict)

	// A keyless submit writes no ledger row at all, so keyless submits must never
	// collide - this is the invariant that lets Commander treat a keyless 23505 as
	// a genuine primary-key failure rather than something to replay.
	require.NoError(t, repo.Insert(ctx, newTask(), nil))
	require.NoError(t, repo.Insert(ctx, newTask(), nil))
}

// TestRepo_GetMissingIsErrNotFound pins the other half of translate().
func TestRepo_GetMissingIsErrNotFound(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	repo := repotasks.New(txmgr.New(pool))

	_, err := repo.Get(context.Background(), uuid.Must(uuid.NewV7()))
	require.ErrorIs(t, err, domaintasks.ErrNotFound)

	_, err = repo.GetByIdempotencyKey(context.Background(), "never-used")
	require.ErrorIs(t, err, domaintasks.ErrNotFound)
}

// TestRepo_PersistTransition_StaleEpochReturnsZero: simulate a claim (stamp
// lease_epoch=1, status=running), transition the aggregate, then fence on a
// stale epoch 99 — the epoch a reclaim would have bumped past us.
func TestRepo_PersistTransition_StaleEpochReturnsZero(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := context.Background()

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
	pool := startPostgresWithMigrations(t)
	txm := txmgr.New(pool)
	tasksRepo := repotasks.New(txm)
	outboxRepo := outbox.New(txm)
	ctx := context.Background()

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
	// they survive it. This is the subtest the T2 red-run proof turns red.
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
