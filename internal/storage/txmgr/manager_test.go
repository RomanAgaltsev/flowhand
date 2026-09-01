//go:build integration

package txmgr_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/RomanAgaltsev/flowhand/internal/config"
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
	// above is the one the manager under test runs on.
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

// insertTaskSQL writes a minimally-valid row into the real `tasks` table.
// Every NOT NULL column whose default migration 00003 dropped is supplied
// explicitly; the rest take their column defaults.
const insertTaskSQL = `INSERT INTO tasks (
	id, handler, tenant_id, payload, priority,
	earliest_at, attempt, max_attempts, shard_id, cancel_requested, updated_at
) VALUES (
	$1, 'txmgr-test', $2, '{}'::jsonb, 4,
	now(), 0, 25, 0, false, now()
)`

// insertTask writes through the manager's Resolver, the way a repository
// inside fn does. Whether it lands in the surrounding transaction or in its
// own implicit one is exactly what these tests observe from outside.
func insertTask(ctx context.Context, m *txmgr.Manager, id uuid.UUID) error {
	_, err := m.Resolve(ctx).Exec(ctx, insertTaskSQL, id, uuid.Nil)
	return err
}

// taskCount reads row count on the pool, outside any transaction, so it only
// sees committed work.
func taskCount(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT count(*) FROM tasks").Scan(&n))
	return n
}

// readTxID observes which transaction the Resolver's DBTX is attached to.
// txid_current() is per-transaction, so two reads on the same transaction
// agree and two on different transactions do not.
func readTxID(ctx context.Context, m *txmgr.Manager, dest *int64) error {
	return m.Resolve(ctx).QueryRow(ctx, "SELECT txid_current()").Scan(dest)
}

// TestWithinTx_HappyPath is the baseline: both writes commit and are visible
// after WithinTx returns. Every other test here is a deviation from it.
func TestWithinTx_HappyPath(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	m := txmgr.New(pool)
	ctx := context.Background()

	require.NoError(t, m.WithinTx(ctx, func(ctx context.Context) error {
		if err := insertTask(ctx, m, uuid.Must(uuid.NewV7())); err != nil {
			return err
		}
		return insertTask(ctx, m, uuid.Must(uuid.NewV7()))
	}))

	require.EqualValues(t, 2, taskCount(t, pool), "both writes must be visible after commit")
}

// TestWithinTx_ErrorRollsBack proves an error return undoes prior writes in
// the same function. This is the property C1's outbox atomicity depends on:
// the task row and its outbox event commit together or not at all.
func TestWithinTx_ErrorRollsBack(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	m := txmgr.New(pool)
	ctx := context.Background()

	wantErr := errors.New("boom")

	err := m.WithinTx(ctx, func(ctx context.Context) error {
		if err := insertTask(ctx, m, uuid.Must(uuid.NewV7())); err != nil {
			return err
		}
		return wantErr
	})

	require.ErrorIs(t, err, wantErr)
	require.Zero(t, taskCount(t, pool), "the first insert must not survive the rollback")
}

// TestWithinTx_PanicRollsBack proves a panic does not leave an open
// transaction on a pooled connection. The failure mode without the deferred
// rollback is invisible in this test and shows up in an unrelated test later:
// the next borrower of that connection inherits the transaction and behaves
// inexplicably. Hence the write-after-panic assertion at the end.
//
// The recover lives in the TEST, not in WithinTx — recovering there would
// swallow a programmer error and leave the caller believing the transaction
// succeeded.
func TestWithinTx_PanicRollsBack(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	m := txmgr.New(pool)
	ctx := context.Background()

	defer func() {
		require.NotNil(t, recover(), "the panic must escape WithinTx")
		require.Zero(t, taskCount(t, pool), "rollback on panic must undo the insert")
		// The next borrower: without the rollback, this write lands inside the
		// abandoned transaction of whatever connection the pool hands back.
		require.NoError(t, insertTask(ctx, m, uuid.Must(uuid.NewV7())))
		require.EqualValues(t, 1, taskCount(t, pool), "the pool must be usable after a panic rollback")
	}()

	_ = m.WithinTx(ctx, func(ctx context.Context) error {
		if err := insertTask(ctx, m, uuid.Must(uuid.NewV7())); err != nil {
			return err
		}
		panic("boom")
	})
}

// TestWithinTx_NestedReusesTransaction proves nesting does not open a second
// transaction. Asserting only that both writes landed would pass on the naive
// nested implementation right up until it deadlocks; equal transaction ids are
// a direct observation of reuse, not an inference from its side effects.
func TestWithinTx_NestedReusesTransaction(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	m := txmgr.New(pool)
	ctx := context.Background()

	var outerTxID, innerTxID int64

	require.NoError(t, m.WithinTx(ctx, func(ctx context.Context) error {
		if err := readTxID(ctx, m, &outerTxID); err != nil {
			return err
		}
		if err := insertTask(ctx, m, uuid.Must(uuid.NewV7())); err != nil {
			return err
		}
		return m.WithinTx(ctx, func(ctx context.Context) error {
			if err := readTxID(ctx, m, &innerTxID); err != nil {
				return err
			}
			return insertTask(ctx, m, uuid.Must(uuid.NewV7()))
		})
	}))

	require.Equal(t, outerTxID, innerTxID,
		"a nested WithinTx must reuse the outer transaction; a second BEGIN would wait on the outer one's locks")
	require.EqualValues(t, 2, taskCount(t, pool), "both writes must land with the outer commit")
}

// TestResolve_OutsideTxReturnsPool proves the fallback a repository meets on
// every read: outside any WithinTx the Resolver hands back the pool itself,
// and a write through it still works.
func TestResolve_OutsideTxReturnsPool(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	m := txmgr.New(pool)
	ctx := context.Background()

	// Same pointer: "the pool" is the contract, not merely something pool-like.
	require.Same(t, pool, m.Resolve(ctx))

	require.NoError(t, insertTask(ctx, m, uuid.Must(uuid.NewV7())))
	require.EqualValues(t, 1, taskCount(t, pool))
}
