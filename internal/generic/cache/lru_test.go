package cache

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/stretchr/testify/require"
)

func TestLRUCapacity(t *testing.T) {
	const capacity = 10

	lru := NewLRU[int, int](capacity, time.Now)

	for i := range capacity {
		lru.Put(i+1, i+1, 0)
	}
	require.Equal(t, capacity, lru.Len())

	lru.Put(capacity+1, capacity+1, 0)
	require.Equal(t, capacity, lru.Len())

	backItem, ok := lru.Get(1)
	require.False(t, ok)
	require.NotEqual(t, 1, backItem)
}

func TestLRUGetPromotesToMostRecent(t *testing.T) {
	const capacity = 3

	lru := NewLRU[int, string](capacity, nil)

	lru.Put(1, "one", 0)
	lru.Put(2, "two", 0)
	lru.Put(3, "three", 0)

	value, ok := lru.Get(1) // 1 is the oldest; Get must promote it to most-recent
	require.True(t, ok)
	require.Equal(t, "one", value)

	lru.Put(4, "four", 0) // 2 is now the least-recent and must be the evictee

	value, ok = lru.Get(2)
	require.False(t, ok)
	require.Empty(t, value)

	for key, want := range map[int]string{1: "one", 3: "three", 4: "four"} {
		value, ok := lru.Get(key)
		require.True(t, ok)
		require.Equal(t, want, value)
	}
	require.Equal(t, capacity, lru.Len())
}

type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func TestLRUPutWithTTLExpires(t *testing.T) {
	clock := newFakeClock()
	lru := NewLRU[int, string](2, clock.Now)

	lru.Put(1, "one", 50*time.Millisecond)

	clock.Advance(100 * time.Millisecond)

	value, ok := lru.Get(1)
	require.False(t, ok)
	require.Empty(t, value)
	require.Zero(t, lru.Len())
}

func TestLRUPutWithZeroTTLNeverExpires(t *testing.T) {
	clock := newFakeClock()
	lru := NewLRU[int, string](2, clock.Now)

	lru.Put(1, "one", 0)

	clock.Advance(time.Hour)

	value, ok := lru.Get(1)
	require.True(t, ok)
	require.Equal(t, "one", value)
	require.Equal(t, 1, lru.Len())
}

func TestLRUExpiredEntriesDoNotOccupySlots(t *testing.T) {
	t.Run("all entries expired", func(t *testing.T) {
		const capacity = 2

		clock := newFakeClock()
		lru := NewLRU[int, string](capacity, clock.Now)

		lru.Put(1, "expired", 50*time.Millisecond)
		lru.Put(2, "expired", 50*time.Millisecond)

		clock.Advance(time.Second)

		lru.Put(3, "live", 0)

		value, ok := lru.Get(3)
		require.True(t, ok)
		require.Equal(t, "live", value)
		require.Equal(t, 1, lru.Len())
	})

	t.Run("live entry survives while expired ones are reclaimed", func(t *testing.T) {
		const capacity = 3

		clock := newFakeClock()
		lru := NewLRU[int, string](capacity, clock.Now)

		lru.Put(1, "live", 0) // never expires, least-recent
		lru.Put(2, "expired", 50*time.Millisecond)
		lru.Put(3, "expired", 50*time.Millisecond)

		clock.Advance(time.Second)

		lru.Put(4, "live", 0) // must reuse the expired slots, not evict key 1

		value, ok := lru.Get(1)
		require.True(t, ok)
		require.Equal(t, "live", value)

		value, ok = lru.Get(4)
		require.True(t, ok)
		require.Equal(t, "live", value)

		_, ok = lru.Get(2)
		require.False(t, ok)
		_, ok = lru.Get(3)
		require.False(t, ok)

		require.Equal(t, 2, lru.Len())
	})
}

// lruOp is a single generated cache operation: Get when get is true, Put otherwise.
type lruOp struct {
	get bool
	key int
	val int
}

// lruModel is a naive reference implementation: order holds keys most-recent
// first and is bounded at capacity.
type lruModel struct {
	capacity int
	order    []int
	vals     map[int]int
}

func (m *lruModel) put(key, val int) {
	if _, ok := m.vals[key]; ok {
		m.vals[key] = val
		m.moveToFront(key)
		return
	}

	m.order = append(m.order, 0)
	copy(m.order[1:], m.order[:len(m.order)-1])
	m.order[0] = key
	m.vals[key] = val

	if len(m.order) > m.capacity {
		delete(m.vals, m.order[len(m.order)-1])
		m.order = m.order[:len(m.order)-1]
	}
}

func (m *lruModel) get(key int) (int, bool) {
	val, ok := m.vals[key]
	if ok {
		m.moveToFront(key)
	}
	return val, ok
}

func (m *lruModel) moveToFront(key int) {
	for i, existing := range m.order {
		if existing == key {
			copy(m.order[1:i+1], m.order[:i])
			m.order[0] = key
			return
		}
	}
}

func TestLRUMatchesReferenceModel(t *testing.T) {
	properties := gopter.NewProperties(gopter.DefaultTestParameters())

	opGen := gen.Struct(reflect.TypeOf(lruOp{}), map[string]gopter.Gen{
		"get": gen.Bool(),
		"key": gen.IntRange(0, 15),
		"val": gen.IntRange(0, 1000),
	})

	properties.Property("Put/Get sequences match a reference model", prop.ForAll(
		replayOps,
		gen.SliceOf(opGen),
		gen.IntRange(1, 8),
	))

	properties.TestingRun(t)
}

func replayOps(ops []lruOp, capacity int) (bool, error) {
	// Key space slightly larger than capacity so the sequence forces
	// collisions, evictions, and re-insertions.
	universe := capacity + 3

	lru := NewLRU[int, int](capacity, nil)
	model := &lruModel{capacity: capacity, vals: make(map[int]int, capacity)}

	for i, op := range ops {
		key := op.key % universe

		if op.get {
			gotVal, gotOK := lru.Get(key)
			wantVal, wantOK := model.get(key)
			if gotOK != wantOK || gotVal != wantVal {
				return false, fmt.Errorf("op %d: Get(%d) = (%d, %v), model = (%d, %v)",
					i, key, gotVal, gotOK, wantVal, wantOK)
			}
		} else {
			lru.Put(key, op.val, 0)
			model.put(key, op.val)
		}

		if lru.Len() != len(model.order) {
			return false, fmt.Errorf("op %d: Len() = %d, model = %d", i, lru.Len(), len(model.order))
		}
	}

	// Drain-compare every possible key in a fixed order; both sides see the
	// same Get promotions, so they must stay in lockstep.
	for key := range universe {
		gotVal, gotOK := lru.Get(key)
		wantVal, wantOK := model.get(key)
		if gotOK != wantOK || gotVal != wantVal {
			return false, fmt.Errorf("final Get(%d) = (%d, %v), model = (%d, %v)",
				key, gotVal, gotOK, wantVal, wantOK)
		}
	}

	return true, nil
}
