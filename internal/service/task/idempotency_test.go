package task

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
)

// TestRedisStore_LookupFailsThrough pins the store's half of the fail-through
// contract, which no Commander test can reach - they all run against
// fakeIdempStore. Make Lookup return its Redis error and this goes red.
//
// Nothing listens on loopback port 1, so every GET fails at dial: a Redis
// outage without a container. One attempt and a short dial timeout keep it
// fast.
func TestRedisStore_LookupFailsThrough(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1,
	})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	var logs bytes.Buffer
	store := NewRedisIdempotencyStore(rdb, slog.New(slog.NewTextHandler(&logs, nil)))
	key, err := domaintasks.NewIdempotencyKey("k-1")
	require.NoError(t, err)

	id, ok, err := store.Lookup(context.Background(), key)

	require.NoError(t, err, "a Redis outage must not reach the caller")
	assert.False(t, ok)
	assert.Equal(t, uuid.Nil, id)
	assert.Contains(t, logs.String(), "level=WARN",
		"a fail-through must be logged, or a dead Redis looks exactly like a cold cache")
}
