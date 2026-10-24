package task

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
)

// idempotencyKeyPrefix namespaces the cache so a flush of these keys cannot
// touch anything else sharing the Redis instance.
const idempotencyKeyPrefix = "flowhand:idemp:"

// IdempotencyTTL is the cache lifetime.
const IdempotencyTTL = 25 * time.Hour

// redisStore is the IdempotencyStore backed by Redis.
type redisStore struct {
	rdb *redis.Client
	log *slog.Logger
}

// IdempotencyStore is the CACHE store for idempotency keys. The durable half
// is the ledger row that TaskRepo.Insert writes inside transaction.
//
// Lookup never reports a cache failure as an error - see contract on the
// method. Record is best-effort and its error is logged, never returned.
type IdempotencyStore interface {
	Lookup(ctx context.Context, key domaintasks.IdempotencyKey) (uuid.UUID, bool, error)
	Record(ctx context.Context, key domaintasks.IdempotencyKey, taskID uuid.UUID) error
}

// NewRedisIdempotencyStore returns the cache half.
func NewRedisIdempotencyStore(rdb *redis.Client, log *slog.Logger) IdempotencyStore {
	return &redisStore{rdb: rdb, log: log}
}

// Lookup is ADVISORY and never reports a cache failure to its caller.
//
// A miss, a Redis outage and a malformed stored value are all the same answer:
// (uuid.Nil, false, nil) - proceed to the transaction, where the ledger is the
// real gate. The error return exists because a future implementation might need
// it. This one never populates it.
//
// A Redis failure MUST be logged. A silent fail-through makes a permanently
// down Redis look exactly like a cold cache and nobody notices the fast path
// is gone.
func (s *redisStore) Lookup(ctx context.Context, key domaintasks.IdempotencyKey) (uuid.UUID, bool, error) {
	val, err := s.rdb.Get(ctx, cacheKey(key)).Result()
	switch {
	case errors.Is(err, redis.Nil):
		// A clean miss - the only one of the three that is not worth a log line.
		return uuid.Nil, false, nil
	case err != nil:
		// Warn, not Error: the submit still succeeds through the ledger. The
		// *Context variant is what lets the obs handler stamp the trace id.
		s.log.WarnContext(ctx, "idempotency cache lookup failed, falling through to the ledger",
			"err", err)
		return uuid.Nil, false, nil
	}
	id, err := uuid.Parse(val)
	if err != nil {
		s.log.WarnContext(ctx, "idempotency cache holds a malformed task id, falling through to the ledger",
			"idempotency_key", key.String(), "err", err)
		return uuid.Nil, false, nil
	}
	return id, true, nil
}

// Record warms the cache AFTER the transaction commits. Plain SET, never NX:
// NX inside or before the transaction advertises a task id that a rollback
// then erases, which is the cache-poisoning failure.
//
// The caller logs and ignores the error. It is returned rather than swallowed
// here so the decision to ignore it is visible at the call site.
func (s *redisStore) Record(ctx context.Context, key domaintasks.IdempotencyKey, taskID uuid.UUID) error {
	if err := s.rdb.Set(ctx, cacheKey(key), taskID.String(), IdempotencyTTL).Err(); err != nil {
		return fmt.Errorf("record idempotency key: %w", err)
	}
	return nil
}

// cacheKey is the namespaced Redis key for an idempotency key.
func cacheKey(key domaintasks.IdempotencyKey) string {
	return idempotencyKeyPrefix + key.String()
}
