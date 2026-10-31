package task

import (
	"bytes"
	"context"
	"encoding/hex"
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

	rec, ok, err := store.Lookup(context.Background(), domaintasks.HandlerFromPersistence("echo"), key)

	require.NoError(t, err, "a Redis outage must not reach the caller")
	assert.False(t, ok)
	assert.Equal(t, domaintasks.IdempotencyRecord{}, rec)
	assert.Contains(t, logs.String(), "level=WARN",
		"a fail-through must be logged, or a dead Redis looks exactly like a cold cache")
}

func TestCacheKey(t *testing.T) {
	key := func(s string) domaintasks.IdempotencyKey {
		k, err := domaintasks.NewIdempotencyKey(s)
		require.NoError(t, err)
		return k
	}
	h := domaintasks.HandlerFromPersistence

	// D28: the ledger's primary key includes the handler, so the cache key must
	// too - otherwise a warm entry for one handler answers a submit to another.
	assert.NotEqual(t, cacheKey(h("echo"), key("k")), cacheKey(h("other"), key("k")),
		"the same key under two handlers is two submits")

	// Neither part is forbidden a ':'. A bare "{handler}:{key}" join makes these
	// two the same Redis key; the handler's length prefix is what tells them apart.
	assert.NotEqual(t, cacheKey(h("a:b"), key("c")), cacheKey(h("a"), key("b:c")),
		"the join must be unambiguous")

	assert.Equal(t, "flowhand:idemp:4:echo:k-1", cacheKey(h("echo"), key("k-1")))
}

func TestParseStored(t *testing.T) {
	id := uuid.MustParse("018f1e60-0000-7000-8000-000000000001")
	fp := domaintasks.PayloadFingerprint{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	good := id.String() + ":" + hex.EncodeToString(fp[:])

	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"well formed", good, true},
		{"what formatStored writes", formatStored(domaintasks.IdempotencyRecord{TaskID: id, Fingerprint: fp}), true},
		// Every value a pre-C3 build wrote is a bare task id. It must read as a
		// miss, so the first deploy degrades to the ledger instead of failing.
		{"pre-C3 bare id", id.String(), false},
		{"missing colon", id.String() + hex.EncodeToString(fp[:]), false},
		{"bad uuid", "not-a-uuid:" + hex.EncodeToString(fp[:]), false},
		{"short hex", id.String() + ":abcd", false},
		{"long hex", good + "00", false},
		{"not hex", id.String() + ":" + "zz" + hex.EncodeToString(fp[1:]), false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseStored(tt.in)
			assert.Equal(t, tt.ok, ok)
			if !tt.ok {
				// A rejected value must not leak a half-parsed result: the
				// caller treats ok=false as a miss and ignores the rest, and a
				// non-zero record here would be a trap for the next reader.
				assert.Equal(t, domaintasks.IdempotencyRecord{}, got)
				return
			}
			assert.Equal(t, domaintasks.IdempotencyRecord{TaskID: id, Fingerprint: fp}, got)
		})
	}
}
