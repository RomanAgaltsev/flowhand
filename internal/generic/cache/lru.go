package cache

import (
	"container/heap"
	"container/list"
	"sync"
	"time"
)

// noExpiry is the heapIndex of an entry with no TTL, which stays out of the
// expiry heap altogether.
const noExpiry = -1

type entry[K comparable, V any] struct {
	key       K
	value     V
	expiresAt time.Time
	heapIndex int // position in LRU.expiry, or noExpiry
}

// expiryHeap orders the entries that have a TTL, soonest expiry first, so the
// cache can tell what has expired without walking every entry.
type expiryHeap[K comparable, V any] []*entry[K, V]

func (h expiryHeap[K, V]) Len() int { return len(h) }

func (h expiryHeap[K, V]) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }

func (h expiryHeap[K, V]) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIndex = i
	h[j].heapIndex = j
}

// Push and Pop satisfy heap.Interface. The cache only ever calls them through
// container/heap, so x is always the entry type.
func (h *expiryHeap[K, V]) Push(x any) {
	e := x.(*entry[K, V])
	e.heapIndex = len(*h)
	*h = append(*h, e)
}

// Pop removes the last entry, which container/heap has already moved there.
func (h *expiryHeap[K, V]) Pop() any {
	old := *h
	last := len(old) - 1
	e := old[last]
	old[last] = nil // the backing array must not keep an evicted entry alive
	*h = old[:last]
	e.heapIndex = noExpiry
	return e
}

// LRU is a bounded, TTL-aware least-recently-used cache, safe for concurrent
// use. Eviction is lazy: expired entries are reclaimed when the cache is full or
// when Len is called, never by a background goroutine, so an idle cache costs
// nothing to keep around.
//
// Two orders are kept over the same entries: recency in lru, expiry in expiry.
// A full cache is the steady state, so reclaiming has to cost what actually
// expired, not what is stored: every Get waits on the same mutex.
type LRU[K comparable, V any] struct {
	mu       sync.Mutex
	capacity int
	items    map[K]*list.Element
	lru      *list.List
	expiry   expiryHeap[K, V]
	now      func() time.Time
}

// NewLRU returns a cache holding at most capacity entries, or nil if capacity is
// not positive. now supplies the clock TTLs are measured against; pass nil for
// time.Now, or a stub to make expiry deterministic in tests.
func NewLRU[K comparable, V any](capacity int, now func() time.Time) *LRU[K, V] {
	if capacity <= 0 {
		return nil
	}

	if now == nil {
		now = time.Now
	}

	return &LRU[K, V]{
		capacity: capacity,
		items:    make(map[K]*list.Element, capacity),
		lru:      list.New(),
		now:      now,
	}
}

// Get returns the value stored under key and whether it was found. An entry
// past its TTL reports false and is dropped, so it never counts against
// capacity. A hit is promoted to most-recently-used.
func (c *LRU[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}

	item := element.Value.(*entry[K, V])

	if isExpired(c.now(), item.expiresAt) {
		c.removeElement(element)

		var zero V
		return zero, false
	}

	c.lru.MoveToFront(element)
	return item.value, true
}

// Put stores value under key, replacing any existing entry and promoting it to
// most-recently-used. A ttl of zero or less means the entry never expires.
//
// When the cache is over capacity, expired entries are reclaimed first and only
// then is the least-recently-used live entry evicted — otherwise a cache full of
// dead entries would evict live ones.
func (c *LRU[K, V]) Put(key K, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()

	expiresAt := time.Time{}
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}

	if element, ok := c.items[key]; ok {
		item := element.Value.(*entry[K, V])
		item.value = value
		c.setExpiry(item, expiresAt)
		c.lru.MoveToFront(element)
		return
	}

	item := &entry[K, V]{
		key:       key,
		value:     value,
		heapIndex: noExpiry,
	}

	c.items[key] = c.lru.PushFront(item)
	c.setExpiry(item, expiresAt)

	// Expired entries must not occupy slots: reclaim them before evicting
	// anything that is still live.
	if c.lru.Len() > c.capacity {
		c.reclaimExpired(now)
	}

	if c.lru.Len() > c.capacity {
		c.removeOldest()
	}
}

// Delete removes key and reports whether it was present. An expired but
// not-yet-reclaimed entry still counts as present.
func (c *LRU[K, V]) Delete(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.items[key]
	if !ok {
		return false
	}

	c.removeElement(element)
	return true
}

// Len returns the number of live entries, reclaiming expired ones as it counts.
// It therefore takes the lock and mutates — it is not a cheap read.
func (c *LRU[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.reclaimExpired(c.now())

	return c.lru.Len()
}

func (c *LRU[K, V]) removeOldest() {
	element := c.lru.Back()
	if element != nil {
		c.removeElement(element)
	}
}

// reclaimExpired drops what has actually expired, soonest first: O(k log n) for
// k expired entries, and nothing at all when the heap's root is still live.
func (c *LRU[K, V]) reclaimExpired(now time.Time) {
	for len(c.expiry) > 0 && isExpired(now, c.expiry[0].expiresAt) {
		c.removeElement(c.items[c.expiry[0].key])
	}
}

// setExpiry moves item to expiresAt, keeping the heap in step: a re-Put can add a
// TTL, change it, or take it away.
func (c *LRU[K, V]) setExpiry(item *entry[K, V], expiresAt time.Time) {
	item.expiresAt = expiresAt

	switch {
	case expiresAt.IsZero():
		if item.heapIndex != noExpiry {
			heap.Remove(&c.expiry, item.heapIndex)
		}
	case item.heapIndex == noExpiry:
		heap.Push(&c.expiry, item)
	default:
		heap.Fix(&c.expiry, item.heapIndex)
	}
}

func (c *LRU[K, V]) removeElement(element *list.Element) {
	item := element.Value.(*entry[K, V])
	if item.heapIndex != noExpiry {
		heap.Remove(&c.expiry, item.heapIndex)
	}
	delete(c.items, item.key)
	c.lru.Remove(element)
}

func isExpired(now, expiresAt time.Time) bool {
	return !expiresAt.IsZero() && now.After(expiresAt)
}
