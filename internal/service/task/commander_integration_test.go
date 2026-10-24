//go:build integration

package task_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

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
	first, err := commander.Submit(ctx, cmd)
	require.NoError(t, err)
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
	cachedID, hit, err := store.Lookup(ctx, key)
	require.NoError(t, err)
	require.True(t, hit, "Lookup must find what the post-commit Record wrote - do they use the same Redis key?")
	assert.Equal(t, first.ID(), cachedID)

	// 3. Same key, warm cache: answered by phase 1, no transaction opened.
	second, err := commander.Submit(ctx, cmd)
	require.NoError(t, err)
	assert.Equal(t, first.ID(), second.ID())
	assert.Equal(t, 1, tx.n, "a cache hit must not open a transaction")

	// 4. Same key, cache evicted: the ledger is now the only gate. The
	// transaction opens, its 23505 rolls it back, and the replay finds the task.
	require.NoError(t, rdb.FlushDB(ctx).Err())
	third, err := commander.Submit(ctx, cmd)
	require.NoError(t, err)
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
