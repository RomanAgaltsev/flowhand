package task

import (
	"fmt"
	"time"

	"github.com/RomanAgaltsev/flowhand/internal/generic/cache"
	"github.com/google/uuid"
)

// lruCache adapts generic LRU to the TaskCache port. It is a type
// adapter and nothing more: no lock, because cache.LRU is already safe for
// concurrent use and a second mutex here would serialise callers twice for
// no benefit.
type lruCache struct {
	lru *cache.LRU[uuid.UUID, TaskView]
}

// NewLRUCache returns a TaskCache holding at most capacity entries. Exported
// because the composition root builds it; an error rather than G2's nil,
// because a nil LRU would not fail here but at the first Get, under traffic.
func NewLRUCache(capacity int) (TaskCache, error) {
	lru := cache.NewLRU[uuid.UUID, TaskView](capacity, nil)
	if lru == nil {
		return nil, fmt.Errorf("task cache: capacity must be positive, got %d", capacity)
	}
	return &lruCache{lru: lru}, nil
}

// Get returns the cached view for id. NB: G2's Get MUTATES - it promotes the
// entry to the front of the list - which is why this is not, and can never
// become, a read-only operation.
func (c *lruCache) Get(id uuid.UUID) (TaskView, bool) {
	return c.lru.Get(id)
}

// Put stores v under id for ttl. Only terminal views reach here; see Querier.Get.
func (c *lruCache) Put(id uuid.UUID, v TaskView, ttl time.Duration) {
	c.lru.Put(id, v, ttl)
}
