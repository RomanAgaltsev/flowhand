//go:build integration

package task_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool" //nolint:depguard // test fixture: the test owns the database, the Commander still reaches it only through ports
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/repository/outbox"
	repotasks "github.com/RomanAgaltsev/flowhand/internal/repository/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
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

// startRedis brings up a throwaway Redis and returns a client pointed at it.
//
// The core testcontainers API rather than modules/redis: one exposed port and a
// wait strategy is all this needs, and it costs no new go.mod requirement. The
// image matches deploy/compose/docker-compose.yml.
func startRedis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()

	rc, err := testcontainers.Run(
		ctx, "redis:8-alpine",
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").WithStartupTimeout(30*time.Second),
		),
	)
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, rc)

	addr, err := rc.PortEndpoint(ctx, "6379/tcp", "")
	require.NoError(t, err)

	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	require.NoError(t, rdb.Ping(ctx).Err(), "redis must answer before the test starts")

	return rdb
}

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
	n     int
}

func (c *countingTx) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	c.n++
	return c.inner.WithinTx(ctx, fn)
}

// countRows runs a single-value count query.
func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

// The assertion that matters is the REPEATED submit: same key, same response
// task id, and still exactly one row in tasks and one in outbox_events.
// A fake cannot flatter this - the ledger's primary key is doing the work.
//
// The key is submitted three times, because ADR 0005 has two gates and each
// needs its own duplicate: the second submit meets a warm cache and must be
// stopped by phase 1, the third meets an evicted cache and must be stopped by
// the ledger. Only the third proves Redis is a cache and not the gate.
func TestSubmit_SameKeyTwiceYieldsOneTask(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	rdb := startRedis(t)
	ctx := context.Background()

	txm := txmgr.New(pool)
	tasksRepo := repotasks.New(txm)
	tx := &countingTx{inner: txm}
	// Logs to the test's own output, so a fail-through shows up under -v
	// instead of vanishing into slog.Default.
	store := task.NewRedisIdempotencyStore(rdb, slog.New(slog.NewTextHandler(t.Output(), nil)))
	commander := task.NewCommander(tasksRepo, outbox.New(txm), tx, store, acceptAnyHandler{})

	cmd := task.SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"msg":"hi"}`), IdempotencyKey: "k-1",
	}

	// 1. First submit: a new task, in exactly one transaction.
	first, created, err := commander.Submit(ctx, cmd)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, 1, tx.n, "the first submit commits exactly one transaction")

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
	assert.Equal(t, 1, tx.n, "a cache hit must not open a transaction")

	// 4. Same key, cache evicted: the ledger is now the only gate. The
	// transaction opens, its 23505 rolls it back, and the replay finds the task.
	require.NoError(t, rdb.FlushDB(ctx).Err())
	third, created, err := commander.Submit(ctx, cmd)
	require.NoError(t, err)
	assert.False(t, created, "a replay is not a creation")
	assert.Equal(t, first.ID(), third.ID(), "the ledger must replay the original task")
	assert.Equal(t, 2, tx.n, "a cold cache costs one transaction, and the ledger rolls it back")

	// 5. Three submits, one of everything. Counted across the whole table, not
	// by id: a duplicate would carry a NEW id, so `WHERE id = first` can never
	// see one.
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM tasks`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM idempotency_keys`))
	assert.Equal(t, 1, countRows(t, pool, `SELECT count(*) FROM outbox_events`))

	// The one outbox row is the Submitted event and is stamped with this task.
	// T2's D10 resolution: R1 orders the relay by aggregate_id, so a null or
	// wrong one breaks it.
	assert.Equal(t, 1, countRows(t, pool,
		`SELECT count(*) FROM outbox_events WHERE type = 'task.submitted.v1' AND aggregate_id = $1`,
		first.ID()))
}

// TestSubmit_IdempotencyConflict is ADR 0005's acceptance test, end to end:
// the conflict is caught from Redis while it is warm, from the ledger once it
// is not, and still from the ledger after the task has been archived. The
// transaction count tells the paths apart, because a 409 looks the same from
// all three.
func TestSubmit_IdempotencyConflict(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	rdb := startRedis(t)
	ctx := context.Background()

	txm := txmgr.New(pool)
	tx := &countingTx{inner: txm}
	store := task.NewRedisIdempotencyStore(rdb, slog.New(slog.NewTextHandler(t.Output(), nil)))
	commander := task.NewCommander(repotasks.New(txm), outbox.New(txm), tx, store, acceptAnyHandler{})

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
	require.Equal(t, 1, tx.n)

	// 2. A again, keys reordered: the same request. Warm cache, no transaction.
	again, created, err := submit(payloadAReordered)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID(), again.ID())
	assert.Equal(t, 1, tx.n, "a warm replay opens no transaction")

	// 3. Payload B, warm cache: the conflict is decided from Redis alone.
	_, _, err = submit(payloadB)
	requireConflict(err, first.ID(), "warm cache, different payload")
	assert.Equal(t, 1, tx.n, "a warm conflict opens no transaction")

	// 4. B, cache flushed: now the ledger insert is the gate. One transaction
	// opens and rolls back - the write protocol, not a leak.
	require.NoError(t, rdb.FlushDB(ctx).Err())
	_, _, err = submit(payloadB)
	requireConflict(err, first.ID(), "cold cache, different payload")
	assert.Equal(t, 2, tx.n)

	// 5. The task finishes and is archived; cache flushed again. A must still
	// replay (D25): the ledger names the task, and Get finds it in the archive.
	_, err = pool.Exec(ctx, `UPDATE tasks SET status = 'succeeded' WHERE id = $1`, first.ID())
	require.NoError(t, err)
	require.NoError(t, rdb.FlushDB(ctx).Err())
	replayed, created, err := submit(payloadAReordered)
	require.NoError(t, err, "a same-payload resubmit after archival is a replay, not a 500")
	assert.False(t, created)
	assert.Equal(t, first.ID(), replayed.ID())
	assert.Equal(t, domaintasks.StatusSucceeded, replayed.Status())
	assert.Equal(t, 3, tx.n)

	// 6. ...and B is still refused, though the tasks row is gone entirely.
	require.NoError(t, rdb.FlushDB(ctx).Err())
	_, _, err = submit(payloadB)
	requireConflict(err, first.ID(), "after archival, different payload")
	assert.Equal(t, 4, tx.n)

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

// TestSubmit_KeyIsScopedByHandler is D28's regression test. The ledger's
// primary key is (tenant, handler, key), so the same key under another handler
// is another submit. Before C3 the Redis key was the bare idempotency key, and
// the second submit below came back with the FIRST handler's task.
func TestSubmit_KeyIsScopedByHandler(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	rdb := startRedis(t)
	ctx := context.Background()

	txm := txmgr.New(pool)
	store := task.NewRedisIdempotencyStore(rdb, slog.New(slog.NewTextHandler(t.Output(), nil)))
	commander := task.NewCommander(repotasks.New(txm), outbox.New(txm), txm, store, acceptAnyHandler{})

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
