package txmgr

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RomanAgaltsev/flowhand/internal/repository"
)

// Manager is the stub for now. It satisfies repository.Resolver and
// service/task.TxRunner, but opens no transaction: Resolve always hands back
// the pool and WithinTx just calls fn.
//
// WARNING: Insert and Append are therefore NOT atomic for now. A crash
// between them loses the outbox event. Later will be replaced with real
// manager that puts a pgx.Tx in ctx and has Resolve return it.
type Manager struct {
	pool *pgxpool.Pool
}

// New creates new Manager.
func New(pool *pgxpool.Pool) *Manager {
	return &Manager{pool: pool}
}

// Resolve resolves repository DBTX.
func (m *Manager) Resolve(_ context.Context) repository.DBTX {
	return m.pool
}

// WithinTx runs fn function.
func (m *Manager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}
