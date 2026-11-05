//go:build integration

package storage_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/storage"
	"github.com/RomanAgaltsev/flowhand/internal/testutil/pgtest"
)

func TestPool_Ping(t *testing.T) {
	ctx := t.Context()

	pool, err := storage.NewPool(ctx, config.DB{
		DSN:      pgtest.EmptyDSN(t),
		MaxConns: 4,
		MinConns: 1,
	})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	require.NoError(t, pool.Ping(ctx))
}
