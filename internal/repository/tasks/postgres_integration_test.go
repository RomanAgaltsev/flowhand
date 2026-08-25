//go:build integration

package tasks_test

import (
	"context"
	"encoding/json"
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

// TestRepo_InsertThenGet_PersistsHandler closes the gap Task L2's acceptance
// named: every unit test in this package would still pass if `handler` were
// dropped between the mapper and the database, because none of them touch a
// database. This one does.
func TestRepo_InsertThenGet_PersistsHandler(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	repo := repotasks.New(txmgr.New(pool))
	ctx := context.Background()

	want := domaintasks.Submit(
		uuid.Must(uuid.NewV7()), "echo", json.RawMessage(`{"msg":"hi"}`),
		time.Now().UTC().Truncate(time.Microsecond),
	)
	require.NoError(t, repo.Insert(ctx, want, nil))

	got, err := repo.Get(ctx, want.ID())
	require.NoError(t, err)

	assert.Equal(t, want.ID(), got.ID())
	assert.Equal(t, "echo", got.Handler()) // the assertion no unit test can make
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

	newTask := func() domaintasks.Task {
		return domaintasks.Submit(uuid.Must(uuid.NewV7()), "echo", json.RawMessage(`{}`), time.Now().UTC())
	}

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
