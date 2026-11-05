//go:build integration

// Package pgtest is the integration suites' shared fixture: a real Postgres and a
// real Redis, in containers.
//
// One Postgres serves a whole test binary, and each test gets a database of its
// own, cloned from a template that was migrated once. Starting a container costs
// seconds and running the migrations costs more, where a clone costs milliseconds,
// and a database per test keeps the tests as isolated as a container each did.
//
// A package that uses it needs a TestMain calling testutil.VerifyMain, which is
// what stops the container before the leak check runs.
//
// Test-only: nothing in production imports it. The build tag keeps testcontainers
// out of the unit test builds.
package pgtest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/storage"
	"github.com/RomanAgaltsev/flowhand/internal/testutil"
	"github.com/RomanAgaltsev/flowhand/migrations"
)

const (
	// The images match deploy/compose/docker-compose.yml.
	postgresImage = "postgres:18-alpine"
	redisImage    = "redis:8-alpine"

	// templateDB is the database the migrations run in. It is a clone source and
	// nothing else: Postgres refuses to copy a database with an open connection.
	templateDB = "flowhand"

	// stopTimeout bounds terminating the container after the tests have finished.
	stopTimeout = 30 * time.Second
)

// shared starts the package's one Postgres on first use.
var shared = sync.OnceValues(startPostgres)

// nextDB names the databases, which only have to differ within one container.
var nextDB atomic.Int64

// Postgres returns a pool on a fresh database with every migration applied. The
// pool is closed when the test ends.
func Postgres(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := newDatabase(t, "TEMPLATE "+templateDB)
	pool, err := storage.NewPool(t.Context(), config.DB{DSN: dsn, MaxConns: 4, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return pool
}

// EmptyDSN returns the DSN of a fresh database with NO migrations applied, for
// the tests where applying them is the thing under test.
func EmptyDSN(t *testing.T) string {
	t.Helper()
	return newDatabase(t, "")
}

// Redis brings up a throwaway Redis and returns a client pointed at it. A
// container per test, unlike Postgres: few tests use it, and it starts in a
// second.
//
// The core testcontainers API rather than modules/redis: one exposed port and a
// wait strategy is all this needs, and it costs no new go.mod requirement.
func Redis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := t.Context()

	rc, err := testcontainers.Run(
		ctx, redisImage,
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

// newDatabase creates a database with the given CREATE DATABASE options and
// returns its DSN. It is left for the container to take away: dropping it would
// only spend time at the end of every test.
func newDatabase(t *testing.T, options string) string {
	t.Helper()

	base, err := shared()
	require.NoError(t, err, "start postgres")

	admin, err := withDatabase(base, "postgres")
	require.NoError(t, err)

	conn, err := pgx.Connect(t.Context(), admin)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, conn.Close(context.WithoutCancel(t.Context())))
	}()

	// The name is a counter and the options are this package's own constants, and
	// CREATE DATABASE takes no parameters.
	name := fmt.Sprintf("test_%d", nextDB.Add(1))
	_, err = conn.Exec(t.Context(), strings.TrimSpace("CREATE DATABASE "+name+" "+options))
	require.NoError(t, err)

	dsn, err := withDatabase(base, name)
	require.NoError(t, err)

	return dsn
}

// withDatabase points a DSN at another database on the same server.
func withDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// startPostgres brings up the container, migrates the template and registers the
// container's shutdown. It returns the template's DSN.
//
// Background rather than a test's context: the container outlives the test that
// happened to start it.
func startPostgres() (string, error) {
	ctx := context.Background()

	pg, err := postgres.Run(
		ctx, postgresImage,
		postgres.WithDatabase(templateDB),
		postgres.WithUsername("flowhand"),
		postgres.WithPassword("flowhand"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return "", fmt.Errorf("run container: %w", err)
	}
	testutil.AfterRun(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		if err := pg.Terminate(stopCtx); err != nil {
			fmt.Printf("pgtest: terminate postgres: %v\n", err)
		}
	})

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", fmt.Errorf("connection string: %w", err)
	}

	if err := migrate(ctx, dsn); err != nil {
		return "", err
	}
	return dsn, nil
}

// migrate runs through the very provider `flowhand migrate up` uses, rather than a
// second copy of its locker config that would drift from it. Its pool is closed
// before this returns, which is what lets the template be cloned.
func migrate(ctx context.Context, dsn string) (err error) {
	p, closeProvider, err := storage.NewGooseProvider(
		ctx, config.DB{DSN: dsn, MaxConns: 2, MinConns: 1}, migrations.FS,
	)
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	defer func() { err = errors.Join(err, closeProvider()) }()

	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrations must apply cleanly: %w", err)
	}
	return nil
}
