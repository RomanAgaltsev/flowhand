package task

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/generic/cache"
)

// newTestCache is a small real LRU: the adapter is three one-line methods, so
// faking it would test the fake.
func newTestCache(t *testing.T) TaskCache {
	t.Helper()
	c, err := NewLRUCache(16)
	require.NoError(t, err)
	return c
}

// taskIn builds an aggregate in an arbitrary status. FromPersistence is the
// only way to construct one - Submit always yields pending.
func taskIn(t *testing.T, status domaintasks.Status) domaintasks.Task {
	t.Helper()
	at := time.Now().UTC()
	return domaintasks.FromPersistence(
		uuid.Must(uuid.NewV7()), status,
		domaintasks.HandlerFromPersistence("echo"), 0,
		json.RawMessage(`{}`), nil, 0, at, 1, at,
	)
}

func TestQuerierGet_MapsToView(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	created := time.Now().UTC().Truncate(time.Microsecond)
	stored := domaintasks.FromPersistence(id, domaintasks.StatusSucceeded,
		domaintasks.HandlerFromPersistence("echo"), 0,
		json.RawMessage(`{"a":1}`), nil, 0, created, 1, created)

	q := NewQuerier(&fakeTasksRepo{rec: &recorder{}, stored: stored}, newTestCache(t), time.Minute)

	got, err := q.Get(context.Background(), id)
	require.NoError(t, err)

	assert.Equal(t, id, got.ID)
	assert.Equal(t, "succeeded", got.Status) // one vocabulary end to end: domain, database and wire agree
	assert.Equal(t, "echo", got.Handler)
	assert.True(t, created.Equal(got.CreatedAt))
}

func TestQuerierGet_PropagatesNotFound(t *testing.T) {
	q := NewQuerier(&fakeTasksRepo{rec: &recorder{}, storedErr: domaintasks.ErrNotFound},
		newTestCache(t), time.Minute)

	_, err := q.Get(context.Background(), uuid.Must(uuid.NewV7()))
	require.ErrorIs(t, err, domaintasks.ErrNotFound)
}

func TestQuerierGet_TerminalIsCached(t *testing.T) {
	rec := &recorder{}
	tt := taskIn(t, domaintasks.StatusSucceeded)
	q := NewQuerier(&fakeTasksRepo{rec: rec, stored: tt}, newTestCache(t), time.Minute)
	ctx := context.Background()

	first, err := q.Get(ctx, tt.ID())
	require.NoError(t, err)
	second, err := q.Get(ctx, tt.ID())
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Equal(t, []string{"get"}, rec.calls,
		"the second Get must be served from cache, with no repository call")
}

func TestQuerierGet_NonTerminalIsNeverCached(t *testing.T) {
	// The regression net for this whole task. Under the retired
	// write-side-invalidation design this test fails immediately - and it
	// fails BEFORE CE4 exists to notice, which is the point of writing it now.
	rec := &recorder{}
	tt := taskIn(t, domaintasks.StatusRunning)
	q := NewQuerier(&fakeTasksRepo{rec: rec, stored: tt}, newTestCache(t), time.Minute)
	ctx := context.Background()

	_, err := q.Get(ctx, tt.ID())
	require.NoError(t, err)
	_, err = q.Get(ctx, tt.ID())
	require.NoError(t, err)

	assert.Equal(t, []string{"get", "get"}, rec.calls,
		"a running task changes under us; every read must reach the repository")
}

func TestQuerierGet_CachedEntryExpires(t *testing.T) {
	// TTL bounds MEMORY, not staleness - a terminal view cannot go stale. The
	// assertion is that expiry reads THROUGH rather than returning nothing.
	rec := &recorder{}
	tt := taskIn(t, domaintasks.StatusSucceeded)

	now := time.Now().UTC()
	clock := func() time.Time { return now }
	lru := &lruCache{lru: cache.NewLRU[uuid.UUID, TaskView](16, clock)}
	q := NewQuerier(&fakeTasksRepo{rec: rec, stored: tt}, lru, time.Minute)
	ctx := context.Background()

	_, err := q.Get(ctx, tt.ID())
	require.NoError(t, err)
	now = now.Add(2 * time.Minute) // past the TTL

	got, err := q.Get(ctx, tt.ID())
	require.NoError(t, err)
	assert.Equal(t, tt.ID(), got.ID)
	assert.Equal(t, []string{"get", "get"}, rec.calls,
		"an expired entry must read through, not serve stale")
}

func TestQuerierGet_MissIsNotCached(t *testing.T) {
	// A miss must not be cached either - otherwise a task submitted a moment
	// later would keep reporting not-found for the whole TTL.
	rec := &recorder{}
	q := NewQuerier(&fakeTasksRepo{rec: rec, storedErr: domaintasks.ErrNotFound},
		newTestCache(t), time.Minute)
	ctx := context.Background()

	id := uuid.Must(uuid.NewV7())
	_, err := q.Get(ctx, id)
	require.ErrorIs(t, err, domaintasks.ErrNotFound)
	_, err = q.Get(ctx, id)
	require.ErrorIs(t, err, domaintasks.ErrNotFound)

	assert.Equal(t, []string{"get", "get"}, rec.calls, "a miss must not be cached")
}

func TestNewLRUCache_RejectsNonPositiveCapacity(t *testing.T) {
	// G2 returns a nil LRU for capacity <= 0, which would not fail until the
	// first Get - under traffic. The constructor turns that into a startup error.
	for _, capacity := range []int{0, -1} {
		_, err := NewLRUCache(capacity)
		require.Error(t, err, "capacity %d", capacity)
	}
}
