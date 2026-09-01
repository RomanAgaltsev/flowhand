package txmgr

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RomanAgaltsev/flowhand/internal/repository"
)

// ctxKey is an unexported empty struct: two packages cannot collide on it the way
// they can on a string key and nothing outside this package can even name it.
type ctxKey struct{}

// Compile-time proof that Manager satisfies repository.Resolver. The
// task.TxRunner assertion lives in ports_test.go: asserting it here would
// import service/task from production storage code, which .go-arch-lint
// rejects — the arch file is the written-down import graph, and an
// infrastructure→service arrow in it would license every future one.
var _ repository.Resolver = (*Manager)(nil)

// Manager is a transaction manager.
type Manager struct {
	pool *pgxpool.Pool
}

// New creates new Manager.
func New(pool *pgxpool.Pool) *Manager {
	return &Manager{pool: pool}
}

// Resolve satisfies repository.Resolver. Returns the tx attached to ctx if one is
// active, otherwise the pool. Repositories call this and never learn which they got -
// pgx.Tx and *pgxpool.Pool both satisfy repository.DBTX.
func (m *Manager) Resolve(ctx context.Context) repository.DBTX {
	if tx, ok := ctx.Value(ctxKey{}).(pgx.Tx); ok {
		return tx
	}
	return m.pool
}

// WithinTx satisfies service/task.TxRunner: it runs fn inside a transaction
// with default options. Isolation stays off the service port because choosing
// a level is a persistence decision, not a use-case one; callers that need an
// explicit level use WithinTxOpts.
func (m *Manager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return m.WithinTxOpts(ctx, pgx.TxOptions{}, fn)
}

// WithinTxOpts rolls back on error or panic, commits on success. If a transaction is
// already attached to ctx, fn runs with existing one and no new tx is begun.
func (m *Manager) WithinTxOpts(ctx context.Context, opts pgx.TxOptions, fn func(context.Context) error) error {
	// Reuse, not nest. A second Begin here would wait on locks the outer tx holds,
	// on a connection the outer tx has checked out - a hang, not an error.
	if _, alreadyInTx := ctx.Value(ctxKey{}).(pgx.Tx); alreadyInTx {
		return fn(ctx)
	}

	// BeginTxFunc owns commit/rollback INCLUDING on panic. Hand-rolling this means a
	// deferred rollback that must not fire after a successful commit - easy to get
	// subtly wrong and the failure mode is a leaked open transaction on a pooled
	// connection that the next borrower inherits.
	return pgx.BeginTxFunc(ctx, m.pool, opts, func(tx pgx.Tx) error {
		return fn(context.WithValue(ctx, ctxKey{}, tx))
	})
}
