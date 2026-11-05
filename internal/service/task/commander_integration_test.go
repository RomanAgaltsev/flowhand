//go:build integration

package task_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool" //nolint:depguard // test fixture: the test owns the database, the Commander still reaches it only through ports
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/repository/outbox"
	repotasks "github.com/RomanAgaltsev/flowhand/internal/repository/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
	"github.com/RomanAgaltsev/flowhand/internal/storage/txmgr"
	"github.com/RomanAgaltsev/flowhand/internal/testutil/pgtest"
)

// acceptAnyHandler stands in for the worker registry, the same role the
// composition root's acceptAnyHandler plays until the worker runtime lands.
type acceptAnyHandler struct{}

func (acceptAnyHandler) Has(string) bool { return true }

// countingTx counts the transactions the Commander opens. It is how this test
// tells apart the two ways a duplicate can be answered: the fast path opens
// none, the ledger replay opens one and rolls it back. Both return the same
// task, so the response alone cannot tell them apart.
type countingTx struct {
	inner task.TxRunner
	n     atomic.Int64
}

func (c *countingTx) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	c.n.Add(1)
	return c.inner.WithinTx(ctx, fn)
}

// countRows runs a single-value count query.
func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(t.Context(), query, args...).Scan(&n))
	return n
}

// The assertion that matters is the REPEATED submit: same key, same response
// task id, and still exactly one row in tasks and one in outbox_events.
// A fake cannot flatter this - the ledger's primary key is doing the work.
//
// The key is submitted three times, because there are two gates and each
// needs its own duplicate: the second submit meets a warm cache and must be
// stopped by phase 1, the third meets an evicted cache and must be stopped by
// the ledger. Only the third proves Redis is a cache and not the gate.
func TestSubmit_SameKeyTwiceYieldsOneTask(t *testing.T) {
	pool := pgtest.Postgres(t)
	rdb := pgtest.Redis(t)
	ctx := t.Context()

	txm := txmgr.New(pool)
	tasksRepo := repotasks.New(txm)
	tx := &countingTx{inner: txm}
	// Logs to the test's own output, so a fail-through shows up under -v
	// instead of vanishing into slog.Default.
	store := task.NewRedisIdempotencyStore(rdb, slog.New(slog.NewTextHandler(t.Output(), nil)))
	commander := task.NewCommander(tasksRepo, outbox.New(txm), tx, store, acceptAnyHandler{}, nil)

	cmd := task.SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"msg":"hi"}`), IdempotencyKey: "k-1",
	}

	// 1. First submit: a new task, in exactly one transaction.
	first, created, err := commander.Submit(ctx, cmd)
	require.NoError(t, err)
	require.True(t, created)
	require.EqualValues(t, 1, tx.n.Load(), "the first submit commits exactly one transaction")

	stored, err := tasksRepo.Get(ctx, first.ID())
	require.NoError(t, err, "the task id Submit returned must name a committed row")
	assert.Equal(t, domaintasks.StatusPending, stored.Status())
	assert.Equal(t, domaintasks.HandlerFromPersistence("echo"), stored.Handler())
	assert.JSONEq(t, `{"msg":"hi"}`, string(stored.Payload()))

	// 2. Phase 3 really reached Redis: what Record wrote, Lookup reads back.
	// The unit fakes cannot check this, because they never share a key space -
	// a store whose Record and Lookup disagree on the Redis key passes every one
	// of them, and leaves the fast path permanently cold.
	key, err := domaintasks.NewIdempotencyKey(cmd.IdempotencyKey)
	require.NoError(t, err)
	cached, hit, err := store.Lookup(ctx, stored.Handler(), key)
	require.NoError(t, err)
	require.True(t, hit, "Lookup must find what the post-commit Record wrote - do they use the same Redis key?")
	assert.Equal(t, first.ID(), cached.TaskID)

	// 3. Same key, warm cache: answered by phase 1, no transaction opened.
	second, created, err := commander.Submit(ctx, cmd)
	require.NoError(t, err)
	assert.False(t, created, "a replay is not a creation")
	assert.Equal(t, first.ID(), second.ID())
	assert.EqualValues(t, 1, tx.n.Load(), "a cache hit must not open a transaction")

	// 4. Same key, cache evicted: the ledger is now the only gate. The
	// transaction opens, its 23505 rolls it back, and the replay finds the task.
	require.NoError(t, rdb.FlushDB(ctx).Err())
	third, created, err := commander.Submit(ctx, cmd)
	require.NoError(t, err)
	assert.False(t, created, "a replay is not a creation")
	assert.Equal(t, first.ID(), third.ID(), "the ledger must replay the original task")
	assert.EqualValues(t, 2, tx.n.Load(), "a cold cache costs one transaction, and the ledger rolls it back")

	// 5. Three submits, one of everything. Counted across the whole table, not
	// by id: a duplicate would carry a NEW id, so `WHERE id = first` can never
	// see one.
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM tasks`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM idempotency_keys`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM outbox_events`))

	// The one outbox row is the Submitted event and is stamped with this task.
	// The relay orders by aggregate_id, so a null or wrong one breaks it.
	assert.Equal(t, 1, countRows(t, pool,
		`SELECT count(*) FROM outbox_events WHERE type = 'task.submitted.v1' AND aggregate_id = $1`,
		first.ID()))
}

// TestSubmit_IdempotencyConflict is the acceptance test, end to end:
// the conflict is caught from Redis while it is warm, from the ledger once it
// is not, and still from the ledger after the task has been archived. The
// transaction count tells the paths apart, because a 409 looks the same from
// all three.
func TestSubmit_IdempotencyConflict(t *testing.T) {
	pool := pgtest.Postgres(t)
	rdb := pgtest.Redis(t)
	ctx := t.Context()

	txm := txmgr.New(pool)
	tx := &countingTx{inner: txm}
	store := task.NewRedisIdempotencyStore(rdb, slog.New(slog.NewTextHandler(t.Output(), nil)))
	commander := task.NewCommander(repotasks.New(txm), outbox.New(txm), tx, store, acceptAnyHandler{}, nil)

	// A is sent first in a non-canonical order. Were it already canonical, a ledger
	// that hashed raw bytes would pass every step below by coincidence.
	const payloadA, payloadAReordered, payloadB = `{"b":[1,2], "a":1}`, `{"a":1,"b":[1,2]}`, `{"a":2}`
	submit := func(payload string) (domaintasks.Task, bool, error) {
		return commander.Submit(ctx, task.SubmitCommand{
			Handler: "echo", Payload: json.RawMessage(payload), IdempotencyKey: "k-1",
		})
	}
	requireConflict := func(err error, existing uuid.UUID, msgAndArgs ...any) {
		t.Helper()
		ce, ok := errors.AsType[*task.ConflictError](err)
		require.True(t, ok, msgAndArgs...)
		assert.Equal(t, existing, ce.ExistingTaskID)
	}

	// 1. Payload A creates the task.
	first, created, err := submit(payloadA)
	require.NoError(t, err)
	require.True(t, created)
	require.EqualValues(t, 1, tx.n.Load())

	// 2. A again, keys reordered: the same request. Warm cache, no transaction.
	again, created, err := submit(payloadAReordered)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID(), again.ID())
	assert.EqualValues(t, 1, tx.n.Load(), "a warm replay opens no transaction")

	// 3. Payload B, warm cache: the ledger confirms what Redis says, and no
	// transaction opens.
	_, _, err = submit(payloadB)
	requireConflict(err, first.ID(), "warm cache, different payload")
	assert.EqualValues(t, 1, tx.n.Load(), "a warm conflict opens no transaction")

	// 4. B, cache flushed: now the ledger insert is the gate. One transaction
	// opens and rolls back - the write protocol, not a leak.
	require.NoError(t, rdb.FlushDB(ctx).Err())
	_, _, err = submit(payloadB)
	requireConflict(err, first.ID(), "cold cache, different payload")
	assert.EqualValues(t, 2, tx.n.Load())

	// 5. The task finishes and is archived; cache flushed again. A must still
	// replay: the ledger names the task, and Get finds it in the archive.
	_, err = pool.Exec(ctx, `UPDATE tasks SET status = 'succeeded' WHERE id = $1`, first.ID())
	require.NoError(t, err)
	require.NoError(t, rdb.FlushDB(ctx).Err())
	replayed, created, err := submit(payloadAReordered)
	require.NoError(t, err, "a same-payload resubmit after archival is a replay, not a 500")
	assert.False(t, created)
	assert.Equal(t, first.ID(), replayed.ID())
	assert.Equal(t, domaintasks.StatusSucceeded, replayed.Status())
	assert.EqualValues(t, 3, tx.n.Load())

	// 6. ...and B is still refused, though the tasks row is gone entirely.
	require.NoError(t, rdb.FlushDB(ctx).Err())
	_, _, err = submit(payloadB)
	requireConflict(err, first.ID(), "after archival, different payload")
	assert.EqualValues(t, 4, tx.n.Load())

	// 7. Six submits, one of everything - and the ledger holds the CANONICAL
	// fingerprint, which is what made steps 2 and 5 replays.
	assert.Equal(t, 0, countRows(t, pool, `SELECT count(*) FROM tasks`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM tasks_archive`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM idempotency_keys`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM outbox_events`))
	var hash []byte
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT payload_hash FROM idempotency_keys WHERE idempotency_key = 'k-1'`).Scan(&hash))
	want, err := domaintasks.NewPayloadFingerprint(json.RawMessage(payloadAReordered))
	require.NoError(t, err)
	assert.Equal(t, want[:], hash)
}

// TestSubmit_KeyIsScopedByHandler is a regression test. The ledger's primary key
// is (tenant, handler, key), so the same key under another handler is another
// submit. When the Redis key was the bare idempotency key, the second submit
// below came back with the FIRST handler's task.
func TestSubmit_KeyIsScopedByHandler(t *testing.T) {
	pool := pgtest.Postgres(t)
	rdb := pgtest.Redis(t)
	ctx := t.Context()

	txm := txmgr.New(pool)
	store := task.NewRedisIdempotencyStore(rdb, slog.New(slog.NewTextHandler(t.Output(), nil)))
	commander := task.NewCommander(repotasks.New(txm), outbox.New(txm), txm, store, acceptAnyHandler{}, nil)

	submit := func(handler string) domaintasks.Task {
		t.Helper()
		got, created, err := commander.Submit(ctx, task.SubmitCommand{
			Handler: handler, Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1",
		})
		require.NoError(t, err)
		require.True(t, created, "%s: the first submit under this handler creates", handler)
		return got
	}

	echo := submit("echo")
	other := submit("other") // the cache is warm for echo's k-1 here

	assert.NotEqual(t, echo.ID(), other.ID())
	assert.Equal(t, "other", other.Handler().Name(), "a warm entry for one handler must not answer another")
	assert.Equal(t, 2, countRows(t, pool, `SELECT count(*) FROM idempotency_keys`))
}

// missingStore is an IdempotencyStore that never hits, so every racer below is
// decided by the ledger's primary key and not by Redis.
type missingStore struct{}

func (missingStore) Lookup(context.Context, domaintasks.Handler, domaintasks.IdempotencyKey) (domaintasks.IdempotencyRecord, bool, error) {
	return domaintasks.IdempotencyRecord{}, false, nil
}

func (missingStore) Record(context.Context, domaintasks.Handler, domaintasks.IdempotencyKey, domaintasks.IdempotencyRecord) error {
	return nil
}

// TestSubmit_ConcurrentSameKey is the race the ledger exists for: N submits
// with one key, released together. Exactly one creates; the rest wait on the
// open transaction's primary-key entry, collide after its commit, and replay.
func TestSubmit_ConcurrentSameKey(t *testing.T) {
	pool := pgtest.Postgres(t)
	txm := txmgr.New(pool)
	commander := task.NewCommander(repotasks.New(txm), outbox.New(txm), txm, missingStore{}, acceptAnyHandler{}, nil)

	const n = 16
	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		ids     [n]uuid.UUID
		created [n]bool
		errs    [n]error
	)
	for i := range n {
		wg.Go(func() {
			<-start
			tk, ok, err := commander.Submit(t.Context(), task.SubmitCommand{
				Handler: "echo", Payload: json.RawMessage(`{"a":1}`), IdempotencyKey: "race-1",
			})
			ids[i], created[i], errs[i] = tk.ID(), ok, err
		})
	}
	close(start)
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i], "racer %d", i)
		assert.Equal(t, ids[0], ids[i], "every racer must name the same task")
	}
	assert.Equal(t, 1, countTrue(created[:]), "exactly one racer creates")
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM tasks`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM idempotency_keys`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM outbox_events`))
}

// TestSubmit_ConcurrentDifferentPayloads is the same race with a client bug in it:
// every racer reuses one key with its own payload. One creates, and every other
// must be refused with the winner's id, not handed the winner's task.
func TestSubmit_ConcurrentDifferentPayloads(t *testing.T) {
	pool := pgtest.Postgres(t)
	txm := txmgr.New(pool)
	commander := task.NewCommander(repotasks.New(txm), outbox.New(txm), txm, missingStore{}, acceptAnyHandler{}, nil)

	const n = 16
	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		ids     [n]uuid.UUID
		created [n]bool
		errs    [n]error
	)
	for i := range n {
		wg.Go(func() {
			<-start
			tk, ok, err := commander.Submit(t.Context(), task.SubmitCommand{
				Handler: "echo", Payload: json.RawMessage(fmt.Sprintf(`{"a":%d}`, i)), IdempotencyKey: "race-2",
			})
			ids[i], created[i], errs[i] = tk.ID(), ok, err
		})
	}
	close(start)
	wg.Wait()

	require.Equal(t, 1, countTrue(created[:]), "exactly one racer creates")
	winner := ids[slices.Index(created[:], true)]

	for i := range n {
		if created[i] {
			require.NoError(t, errs[i])
			continue
		}
		ce, ok := errors.AsType[*task.ConflictError](errs[i])
		require.True(t, ok, "racer %d: got %v", i, errs[i])
		assert.Equal(t, winner, ce.ExistingTaskID)
	}
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM tasks`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM idempotency_keys`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM outbox_events`))
}

// countTrue counts the racers that report having created the task.
func countTrue(flags []bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

// TestSubmit_PayloadBoundaryIsStorable closes the loop on the domain's payload
// limits. The unit tests prove what is refused; only a real JSONB column can prove
// that everything let through is actually stored, which is what makes the refusals
// the whole of the client-triggerable 500 and not just part of it.
func TestSubmit_PayloadBoundaryIsStorable(t *testing.T) {
	pool := pgtest.Postgres(t)
	txm := txmgr.New(pool)
	commander := task.NewCommander(repotasks.New(txm), outbox.New(txm), txm, missingStore{}, acceptAnyHandler{}, nil)

	tests := []struct {
		name    string
		payload string
	}{
		{"largest double", `{"n":1.7976931348623157e308}`},
		{"smallest exponent allowed", `{"n":1e-999}`},
		{"zero at the exponent bound", `{"n":0e-999}`},
		{"long exact decimal", `{"n":0.` + strings.Repeat("1", 1000) + `}`},
		{"long integer", `{"n":` + strings.Repeat("9", 300) + `}`},
		{"literal backslash-u", `{"s":"\\u0000"}`},
		{"astral plane", `{"s":"😀"}`},
		{"as deep as the canonicalizer allows", strings.Repeat(`[`, 9990) + strings.Repeat(`]`, 9990)},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, created, err := commander.Submit(t.Context(), task.SubmitCommand{
				Handler: "echo", Payload: json.RawMessage(tt.payload), IdempotencyKey: fmt.Sprintf("k-%d", i),
			})
			require.NoError(t, err, "the domain let this through, so Postgres must take it")
			assert.True(t, created)
		})
	}
}
