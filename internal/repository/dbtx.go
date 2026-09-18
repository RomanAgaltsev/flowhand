package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the surface that sqlc-generated queries take. Both *pgxpool.Pool
// and pgx.Tx satisfy it.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	// SendBatch is what sqlc's :batchexec annotation calls (outbox append).
	// Both *pgxpool.Pool and pgx.Tx implement it, so the resolver still hands
	// back either. CopyFrom stays excluded: pgx.Tx does not have it.
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// Resolver returns the DBTX bound to ctx if a transaction is active,
// or the underlying connection pool otherwise. Implemented by
// internal/storage/txmgr.Manager.
type Resolver interface {
	Resolve(ctx context.Context) DBTX
}
