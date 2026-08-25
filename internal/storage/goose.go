package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

// NewGooseProvider builds the migration provider that every entry point shares:
// the `flowhand migrate` subcommand, and the tests that need a migrated
// database. It owns its own pool, so the caller gets one cleanup func for both.
//
// It lives in this package rather than next to the CLI command because this is
// a layer allowed to name pgx (.golangci.yml, depguard `tx-isolation`). fsys is
// injected rather than imported so that nothing under internal/ has to depend
// on the root-level migrations package.
//
// pgbouncer: session-level advisory locks are tied to ONE backend connection.
// Under transaction-pooling mode the connection you locked on is not the
// connection you get back, so this path must bypass pgbouncer and talk to
// Postgres directly.
func NewGooseProvider(ctx context.Context, cfg config.DB, fsys fs.FS) (*goose.Provider, func() error, error) {
	pool, err := NewPool(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("pool: %w", err)
	}

	// A goose run spans one transaction PER MIGRATION. A transaction-scoped lock
	// would release between them and reopen the race window, so the locker must be
	// session-scoped: acquired once, held across every migration in the run.
	// Period is deliberately short. The product (period x threshold) is the
	// ceiling — 30min, generous enough for a migration that rewrites a large
	// table — but the PERIOD is what a waiting replica pays after the lock
	// actually frees. At goose's 5s default a replica idles ~5s past a migration
	// that took 200ms; at 15s, three times that. 2s keeps the same ceiling and
	// costs one pg_try_advisory_lock every 2s, which is nothing.
	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockTimeout(2, 900), // retry every 2s, up to 900 times ~30min
	)
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("locker: %w", err)
	}

	// goose speaks database/sql; pgx provides the adapter over the same pool.
	db := stdlib.OpenDBFromPool(pool)

	p, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		fsys,
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		closeErr := db.Close()
		pool.Close()
		return nil, nil, errors.Join(fmt.Errorf("provider: %w", err), closeErr)
	}

	// db first: its connections return to the pool before the pool closes, which
	// is what actually releases the session advisory lock.
	cleanup := func() error {
		closeErr := db.Close()
		pool.Close()
		return closeErr
	}

	return p, cleanup, nil
}
