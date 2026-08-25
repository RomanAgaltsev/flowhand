package cache

import (
	"container/list"
	"sync"
	"time"
)

type entry[K comparable, V any] struct {
	key       K
	value     V
	expiresAt time.Time
}

// LRU is a bounded, TTL-aware least-recently-used cache, safe for concurrent
// use. Eviction is lazy: expired entries are reclaimed when the cache is full or
// when Len is called, never by a background goroutine, so an idle cache costs
// nothing to keep around.
type LRU[K comparable, V any] struct {
	mu       sync.Mutex
	capacity int
	items    map[K]*list.Element
	lru      *list.List
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

	expiresAt := time.Time{}
	if ttl > 0 {
		expiresAt = c.now().Add(ttl)
	}

	if element, ok := c.items[key]; ok {
		item := element.Value.(*entry[K, V])
		item.value = value
		item.expiresAt = expiresAt
		c.lru.MoveToFront(element)
		return
	}

	item := &entry[K, V]{
		key:       key,
		value:     value,
		expiresAt: expiresAt,
	}

	element := c.lru.PushFront(item)
	c.items[key] = element

	// Expired entries must not occupy slots: reclaim them before evicting
	// anything that is still live.
	if c.lru.Len() > c.capacity {
		c.removeExpired()
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

	c.removeExpired()

	return c.lru.Len()
}

func (c *LRU[K, V]) removeOldest() {
	element := c.lru.Back()
	if element != nil {
		c.removeElement(element)
	}
}

func (c *LRU[K, V]) removeExpired() {
	for element := c.lru.Front(); element != nil; {
		next := element.Next()
		if isExpired(c.now(), element.Value.(*entry[K, V]).expiresAt) {
			c.removeElement(element)
		}
		element = next
	}
}

func (c *LRU[K, V]) removeElement(element *list.Element) {
	item := element.Value.(*entry[K, V])
	delete(c.items, item.key)
	c.lru.Remove(element)
}

func isExpired(now, expiresAt time.Time) bool {
	return !expiresAt.IsZero() && now.After(expiresAt)
}
