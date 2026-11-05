package cache

import (
	"fmt"
	"reflect"
	"slices"
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

// lruOp is a single generated cache operation: Get when Get is true, Put otherwise.
//
// The fields are exported because gen.Struct cannot set any others: it skips them
// without a word, and every generated operation is then the zero value.
type lruOp struct {
	Get bool
	Key int
	Val int
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

	opGen := gen.Struct(reflect.TypeFor[lruOp](), map[string]gopter.Gen{
		"Get": gen.Bool(),
		"Key": gen.IntRange(0, 15),
		"Val": gen.IntRange(0, 1000),
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
		key := op.Key % universe

		if op.Get {
			gotVal, gotOK := lru.Get(key)
			wantVal, wantOK := model.get(key)
			if gotOK != wantOK || gotVal != wantVal {
				return false, fmt.Errorf("op %d: Get(%d) = (%d, %v), model = (%d, %v)",
					i, key, gotVal, gotOK, wantVal, wantOK)
			}
		} else {
			lru.Put(key, op.Val, 0)
			model.put(key, op.Val)
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

// ttlModel is the reference for the TTL half, deliberately naive: a slice in
// recency order, scanned in full. TestLRUMatchesReferenceModel only ever Puts with
// no TTL, so it cannot reach the expiry heap at all.
type ttlModel struct {
	capacity int
	order    []int // most recent first
	vals     map[int]int
	expires  map[int]time.Time
	now      time.Time
}

func (m *ttlModel) expired(key int) bool {
	at, ok := m.expires[key]
	return ok && m.now.After(at)
}

func (m *ttlModel) remove(key int) {
	delete(m.vals, key)
	delete(m.expires, key)
	m.order = slices.DeleteFunc(m.order, func(k int) bool { return k == key })
}

func (m *ttlModel) reclaim() {
	for _, key := range slices.Clone(m.order) {
		if m.expired(key) {
			m.remove(key)
		}
	}
}

func (m *ttlModel) promote(key int) {
	m.order = slices.DeleteFunc(m.order, func(k int) bool { return k == key })
	m.order = slices.Insert(m.order, 0, key)
}

func (m *ttlModel) put(key, val int, ttl time.Duration) {
	_, existed := m.vals[key]
	m.vals[key] = val
	delete(m.expires, key)
	if ttl > 0 {
		m.expires[key] = m.now.Add(ttl)
	}
	m.promote(key)

	if existed || len(m.order) <= m.capacity {
		return
	}
	m.reclaim()
	if len(m.order) > m.capacity {
		m.remove(m.order[len(m.order)-1])
	}
}

func (m *ttlModel) get(key int) (int, bool) {
	val, ok := m.vals[key]
	if !ok {
		return 0, false
	}
	if m.expired(key) {
		m.remove(key)
		return 0, false
	}
	m.promote(key)
	return val, true
}

// heapConsistent checks what the model cannot see: every entry with a TTL is in
// the heap at the index it believes it has, and no other entry is.
func heapConsistent(c *LRU[int, int]) error {
	withTTL := 0
	for key, element := range c.items {
		item := element.Value.(*entry[int, int])
		if item.expiresAt.IsZero() {
			if item.heapIndex != noExpiry {
				return fmt.Errorf("key %d has no TTL but heapIndex %d", key, item.heapIndex)
			}
			continue
		}
		withTTL++
		if item.heapIndex < 0 || item.heapIndex >= len(c.expiry) || c.expiry[item.heapIndex] != item {
			return fmt.Errorf("key %d: heapIndex %d does not point back at it", key, item.heapIndex)
		}
	}
	if withTTL != len(c.expiry) {
		return fmt.Errorf("%d entries have a TTL, the heap holds %d", withTTL, len(c.expiry))
	}
	return nil
}

// replayTTLOps decodes each int into one operation, so that a sequence mixes
// every way an entry can gain, change, lose or outlive its TTL.
func replayTTLOps(ops []int, capacity int) (bool, error) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	universe := capacity + 3

	lru := NewLRU[int, int](capacity, func() time.Time { return now })
	model := &ttlModel{
		capacity: capacity, now: start,
		vals: make(map[int]int), expires: make(map[int]time.Time),
	}

	for i, op := range ops {
		key := (op / 8) % universe
		ttl := time.Duration(op/64%4) * time.Second // 0 means no TTL

		switch op % 8 {
		case 0, 1, 2:
			lru.Put(key, op, ttl)
			model.put(key, op, ttl)
		case 3, 4:
			gotVal, gotOK := lru.Get(key)
			wantVal, wantOK := model.get(key)
			if gotOK != wantOK || gotVal != wantVal {
				return false, fmt.Errorf("op %d: Get(%d) = (%d, %v), model = (%d, %v)",
					i, key, gotVal, gotOK, wantVal, wantOK)
			}
		case 5:
			_, want := model.vals[key]
			model.remove(key)
			if got := lru.Delete(key); got != want {
				return false, fmt.Errorf("op %d: Delete(%d) = %v, model = %v", i, key, got, want)
			}
		case 6:
			now = now.Add(time.Second + time.Nanosecond)
			model.now = now
		default:
			model.reclaim()
			if got, want := lru.Len(), len(model.order); got != want {
				return false, fmt.Errorf("op %d: Len() = %d, model = %d", i, got, want)
			}
		}

		if err := heapConsistent(lru); err != nil {
			return false, fmt.Errorf("op %d: %w", i, err)
		}
	}
	return true, nil
}

func TestLRUExpiryMatchesReferenceModel(t *testing.T) {
	params := gopter.DefaultTestParameters()
	params.MinSuccessfulTests = 2000
	properties := gopter.NewProperties(params)

	properties.Property("sequences with TTLs match a reference model", prop.ForAll(
		replayTTLOps,
		gen.SliceOf(gen.IntRange(0, 1<<16)),
		gen.IntRange(1, 8),
	))

	properties.TestingRun(t)
}

// BenchmarkLRU_PutAtCapacity keeps the regression visible: a full cache is the
// steady state, and an insert into one must not cost more as capacity grows.
func BenchmarkLRU_PutAtCapacity(b *testing.B) {
	for _, capacity := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("cap=%d", capacity), func(b *testing.B) {
			c := NewLRU[int, int](capacity, nil)
			for i := range capacity {
				c.Put(i, i, 5*time.Minute)
			}
			next := capacity
			for b.Loop() {
				c.Put(next, next, 5*time.Minute)
				next++
			}
		})
	}
}
