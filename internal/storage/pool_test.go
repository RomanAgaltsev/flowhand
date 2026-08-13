//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/storage"
)

func TestPool_Ping(t *testing.T) {
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

	pool, err := storage.NewPool(
		ctx,
		config.DB{
			DSN:      dsn,
			MaxConns: 4,
			MinConns: 1,
		},
	)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	require.NoError(t, pool.Ping(ctx))
}
