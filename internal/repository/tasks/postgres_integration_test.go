//go:build integration

package tasks_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
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
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := storage.NewPool(ctx, config.DB{DSN: dsn, MaxConns: 4, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// goose speaks database/sql; borrow a *sql.DB over the same pool.
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, goose.SetDialect("postgres"))
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "migrations"))
	require.NoError(t, err)
	require.NoError(t, goose.Up(db, dir), "migrations must apply cleanly")

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
// cannot: that a real Postgres 23505 from the partial unique index actually
// becomes domaintasks.ErrConflict. Delete the SQLSTATE check in postgres.go and
// every unit test still passes while this one fails.
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

	// The partial index ignores NULLs, so keyless submits must never collide -
	// this is the invariant that lets Commander treat a keyless 23505 as a
	// genuine primary-key failure rather than something to replay.
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
