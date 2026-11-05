//go:build integration

package txmgr_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/storage/txmgr"
	"github.com/RomanAgaltsev/flowhand/internal/testutil/pgtest"
)

// insertTaskSQL writes a minimally-valid row into the real `tasks` table.
// Every NOT NULL column without a default is supplied explicitly; the rest take
// their column defaults.
const insertTaskSQL = `INSERT INTO tasks (
	id, status, handler, tenant_id, payload, priority,
	earliest_at, attempt, max_attempts, shard_id, cancel_requested, updated_at
) VALUES (
	$1, 'pending', 'txmgr-test', $2, '{}'::jsonb, 4,
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
func taskCount(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM tasks").Scan(&n))
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
	pool := pgtest.Postgres(t)
	m := txmgr.New(pool)
	ctx := t.Context()

	require.NoError(t, m.WithinTx(ctx, func(ctx context.Context) error {
		if err := insertTask(ctx, m, uuid.Must(uuid.NewV7())); err != nil {
			return err
		}
		return insertTask(ctx, m, uuid.Must(uuid.NewV7()))
	}))

	require.EqualValues(t, 2, taskCount(ctx, t, pool), "both writes must be visible after commit")
}

// TestWithinTx_ErrorRollsBack proves an error return undoes prior writes in
// the same function. This is the property the outbox's atomicity depends on:
// the task row and its outbox event commit together or not at all.
func TestWithinTx_ErrorRollsBack(t *testing.T) {
	pool := pgtest.Postgres(t)
	m := txmgr.New(pool)
	ctx := t.Context()

	wantErr := errors.New("boom")

	err := m.WithinTx(ctx, func(ctx context.Context) error {
		if err := insertTask(ctx, m, uuid.Must(uuid.NewV7())); err != nil {
			return err
		}
		return wantErr
	})

	require.ErrorIs(t, err, wantErr)
	require.Zero(t, taskCount(ctx, t, pool), "the first insert must not survive the rollback")
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
	pool := pgtest.Postgres(t)
	m := txmgr.New(pool)
	ctx := t.Context()

	defer func() {
		require.NotNil(t, recover(), "the panic must escape WithinTx")
		require.Zero(t, taskCount(ctx, t, pool), "rollback on panic must undo the insert")
		// The next borrower: without the rollback, this write lands inside the
		// abandoned transaction of whatever connection the pool hands back.
		require.NoError(t, insertTask(ctx, m, uuid.Must(uuid.NewV7())))
		require.EqualValues(t, 1, taskCount(ctx, t, pool), "the pool must be usable after a panic rollback")
	}()

	_ = m.WithinTx(ctx, func(ctx context.Context) error { //nolint:errcheck // unreachable: fn panics, and the deferred block asserts the outcome
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
	pool := pgtest.Postgres(t)
	m := txmgr.New(pool)
	ctx := t.Context()

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
	require.EqualValues(t, 2, taskCount(ctx, t, pool), "both writes must land with the outer commit")
}

// TestResolve_OutsideTxReturnsPool proves the fallback a repository meets on
// every read: outside any WithinTx the Resolver hands back the pool itself,
// and a write through it still works.
func TestResolve_OutsideTxReturnsPool(t *testing.T) {
	pool := pgtest.Postgres(t)
	m := txmgr.New(pool)
	ctx := t.Context()

	// Same pointer: "the pool" is the contract, not merely something pool-like.
	require.Same(t, pool, m.Resolve(ctx))

	require.NoError(t, insertTask(ctx, m, uuid.Must(uuid.NewV7())))
	require.EqualValues(t, 1, taskCount(ctx, t, pool))
}

// TestWithinTxOpts_NestedIsolation pins what a nested call may ask for. It joins
// the outer transaction, so it runs at the outer level whatever it says: a request
// the outer level satisfies is fine, and one it does not must fail rather than
// silently run weaker than the caller needs.
func TestWithinTxOpts_NestedIsolation(t *testing.T) {
	m := txmgr.New(pgtest.Postgres(t))
	ctx := t.Context()

	tests := []struct {
		name    string
		outer   pgx.TxIsoLevel
		inner   pgx.TxIsoLevel
		refused bool
	}{
		{name: "default inside default", outer: "", inner: ""},
		{name: "read committed inside the default, which is read committed", outer: "", inner: pgx.ReadCommitted},
		{name: "the default inside serializable", outer: pgx.Serializable, inner: ""},
		{name: "weaker inside serializable", outer: pgx.Serializable, inner: pgx.ReadCommitted},
		{name: "same level", outer: pgx.RepeatableRead, inner: pgx.RepeatableRead},
		{name: "serializable inside the default", outer: "", inner: pgx.Serializable, refused: true},
		{name: "serializable inside repeatable read", outer: pgx.RepeatableRead, inner: pgx.Serializable, refused: true},
		{name: "repeatable read inside read committed", outer: pgx.ReadCommitted, inner: pgx.RepeatableRead, refused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ran := false
			err := m.WithinTxOpts(ctx, pgx.TxOptions{IsoLevel: tt.outer}, func(ctx context.Context) error {
				return m.WithinTxOpts(ctx, pgx.TxOptions{IsoLevel: tt.inner}, func(context.Context) error {
					ran = true
					return nil
				})
			})
			if tt.refused {
				require.ErrorIs(t, err, txmgr.ErrIsolationMismatch)
				require.False(t, ran, "a refused call must not run its function")
				return
			}
			require.NoError(t, err)
			require.True(t, ran)
		})
	}
}
