package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

// TestRedisOptions pins the option the idempotency cache's budget depends on.
// TestRedisStore_BudgetBoundsABlackholedRedis proves the budget binds on a client
// that has it; this proves the client production builds does.
func TestRedisOptions(t *testing.T) {
	opts := redisOptions(config.Redis{
		Addr:         "redis:6379",
		DialTimeout:  1 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolTimeout:  4 * time.Second,
	})

	assert.True(t, opts.ContextTimeoutEnabled,
		"without it go-redis ignores the context's deadline and the cache budget does nothing")
	assert.Equal(t, "redis:6379", opts.Addr)
	assert.Equal(t, 1*time.Second, opts.DialTimeout)
	assert.Equal(t, 2*time.Second, opts.ReadTimeout)
	assert.Equal(t, 3*time.Second, opts.WriteTimeout)
	assert.Equal(t, 4*time.Second, opts.PoolTimeout)
}
