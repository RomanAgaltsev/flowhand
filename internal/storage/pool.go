package storage

import (
	"context"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

// NewPool opens an instrumented pgx connection pool from the DB config.
func NewPool(ctx context.Context, cfg config.DB) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	// Override only what the caller actually set. ParseConfig has already
	// applied pgx's own defaults, and a zero field here means "unset", not
	// "zero": pgxpool reads MaxConnLifetime as an absolute deadline
	// (maxAgeTime = now + lifetime), so 0 makes every connection expire the
	// instant it is created. Acquire then destroys and retries until it gives
	// up with "too many failed attempts acquiring connection" - which is what
	// the integration suite hit the first time CI ever ran it.
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pcfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	pcfg.ConnConfig.Tracer = otelpgx.NewTracer()
	return pgxpool.NewWithConfig(ctx, pcfg)
}
