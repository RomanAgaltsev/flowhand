package task

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"sync"
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

	rec, ok, err := store.Lookup(t.Context(), domaintasks.HandlerFromPersistence("echo"), key)

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

	// The ledger's primary key includes the handler, so the cache key must
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
		// Every value an older build wrote is a bare task id. It must read as a
		// miss, so the first deploy degrades to the ledger instead of failing.
		{"bare id from an older build", id.String(), false},
		{"second colon", good + ":00", false},
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

// blackholeRedis accepts connections and never answers: a network partition
// rather than a refused connection. Every byte is read and dropped, so the client
// waits on a reply that will not come.
func blackholeRedis(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				_, _ = io.Copy(io.Discard, conn) //nolint:errcheck // the client closing is how this ends
				_ = conn.Close()                 //nolint:errcheck // nothing to do about it here
			})
		}
	})
	t.Cleanup(func() {
		require.NoError(t, l.Close())
		wg.Wait()
	})

	return l.Addr().String()
}

// TestRedisStore_BudgetBoundsABlackholedRedis pins the latency half of "the cache
// is advisory". A refused connection fails fast, so TestRedisStore_LookupFailsThrough
// cannot see this: only a Redis that stops answering holds a submit, and for as
// long as the client's read timeout unless the budget binds.
//
// The read timeout below is far past the assertion on purpose. It is what a
// submit waits for when the budget is not honored, which is exactly what happens
// with ContextTimeoutEnabled off.
func TestRedisStore_BudgetBoundsABlackholedRedis(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:                  blackholeRedis(t),
		ReadTimeout:           5 * time.Second,
		ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	store := NewRedisIdempotencyStore(rdb, slog.New(slog.DiscardHandler))
	key, err := domaintasks.NewIdempotencyKey("k-1")
	require.NoError(t, err)
	handler := domaintasks.HandlerFromPersistence("echo")

	const limit = time.Second // the budget is 25ms; this is headroom for a slow CI runner

	start := time.Now()
	_, ok, err := store.Lookup(t.Context(), handler, key)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Less(t, time.Since(start), limit, "Lookup must give up at the budget")

	start = time.Now()
	require.Error(t, store.Record(t.Context(), handler, key, domaintasks.IdempotencyRecord{}))
	assert.Less(t, time.Since(start), limit, "Record must give up at the budget")
}

// TestSubmit_BlackholedRedisStillCreates is the same fault seen from the top: the
// submit neither hangs nor fails, because a cache that cannot answer is a cache
// miss and the ledger is the gate.
func TestSubmit_BlackholedRedisStillCreates(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:                  blackholeRedis(t),
		ReadTimeout:           5 * time.Second,
		ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	rec := &recorder{}
	id := uuid.Must(uuid.NewV7())
	c := NewCommander(&fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		NewRedisIdempotencyStore(rdb, slog.New(slog.DiscardHandler)), fakeCatalog{},
		slog.New(slog.DiscardHandler))
	c.newID = func() (uuid.UUID, error) { return id, nil }

	start := time.Now()
	got, created, err := c.Submit(t.Context(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1",
	})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, id, got.ID())
	assert.Less(t, time.Since(start), time.Second, "two budgets, not two read timeouts")
}

func TestNewRedisIdempotencyStore_NilLogDefaults(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	store := NewRedisIdempotencyStore(rdb, nil)
	key, err := domaintasks.NewIdempotencyKey("k-1")
	require.NoError(t, err)

	// Would panic on the first Redis failure, which is the logging path.
	_, ok, err := store.Lookup(t.Context(), domaintasks.HandlerFromPersistence("echo"), key)
	require.NoError(t, err)
	assert.False(t, ok)
}
