package task

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
// is the ledger row that TasksRepo.Insert writes inside the transaction.
//
// A key is scoped by handler, exactly as the ledger's primary key is: the same
// key under two handlers is two submits.
//
// Lookup never reports a cache failure as an error - see contract on the
// method. Record is best-effort and its error is logged, never returned.
type IdempotencyStore interface {
	Lookup(ctx context.Context, handler domaintasks.Handler, key domaintasks.IdempotencyKey) (domaintasks.IdempotencyRecord, bool, error)
	Record(ctx context.Context, handler domaintasks.Handler, key domaintasks.IdempotencyKey, rec domaintasks.IdempotencyRecord) error
}

// NewRedisIdempotencyStore returns the cache half.
func NewRedisIdempotencyStore(rdb *redis.Client, log *slog.Logger) IdempotencyStore {
	return &redisStore{rdb: rdb, log: log}
}

// Lookup is ADVISORY and never reports a cache failure to its caller.
//
// A miss, a Redis outage and a malformed stored value are all the same answer:
// (zero, false, nil) - proceed to the transaction, where the ledger is the
// real gate. The error return exists because a future implementation might need
// it. This one never populates it.
//
// A Redis failure MUST be logged. A silent fail-through makes a permanently
// down Redis look exactly like a cold cache and nobody notices the fast path
// is gone.
func (s *redisStore) Lookup(
	ctx context.Context, handler domaintasks.Handler, key domaintasks.IdempotencyKey,
) (domaintasks.IdempotencyRecord, bool, error) {
	val, err := s.rdb.Get(ctx, cacheKey(handler, key)).Result()
	switch {
	case errors.Is(err, redis.Nil):
		// A clean miss - the only one of the three that is not worth a log line.
		return domaintasks.IdempotencyRecord{}, false, nil
	case err != nil:
		// Warn, not Error: the submit still succeeds through the ledger. The
		// *Context variant is what lets the obs handler stamp the trace id.
		s.log.WarnContext(ctx, "idempotency cache lookup failed, falling through to the ledger",
			"err", err)
		return domaintasks.IdempotencyRecord{}, false, nil
	}
	rec, ok := parseStored(val)
	if !ok {
		// Includes every value a pre-C3 build wrote: a bare task id.
		s.log.WarnContext(ctx, "idempotency cache holds a malformed value, falling through to the ledger",
			"idempotency_key", key.String())
		return domaintasks.IdempotencyRecord{}, false, nil
	}
	return rec, true, nil
}

// Record warms the cache AFTER the transaction commits. Plain SET, never NX:
// NX inside or before the transaction advertises a task id that a rollback
// then erases, which is the cache-poisoning failure.
//
// The caller logs and ignores the error. It is returned rather than swallowed
// here so the decision to ignore it is visible at the call site.
func (s *redisStore) Record(
	ctx context.Context, handler domaintasks.Handler, key domaintasks.IdempotencyKey, rec domaintasks.IdempotencyRecord,
) error {
	if err := s.rdb.Set(ctx, cacheKey(handler, key), formatStored(rec), IdempotencyTTL).Err(); err != nil {
		return fmt.Errorf("record idempotency key: %w", err)
	}
	return nil
}

// cacheKey is the namespaced Redis key for (handler, key). The handler's length
// leads it, because neither the handler nor the key is forbidden a ':' - and
// without the length, handler "a:b" with key "c" and handler "a" with key "b:c"
// would share one Redis key.
func cacheKey(handler domaintasks.Handler, key domaintasks.IdempotencyKey) string {
	return idempotencyKeyPrefix + strconv.Itoa(len(handler.Name())) + ":" + handler.Name() + ":" + key.String()
}

// formatStored writes the Redis value: "{task_id}:{hex fingerprint}".
func formatStored(rec domaintasks.IdempotencyRecord) string {
	return rec.TaskID.String() + ":" + hex.EncodeToString(rec.Fingerprint[:])
}

// parseStored reads a Redis value back. Parse DEFENSIVELY: a value written by
// an older build, or a truncated write, must degrade to a cache miss rather
// than an error - the ledger is the authority and answers correctly either way.
func parseStored(s string) (domaintasks.IdempotencyRecord, bool) {
	splitted := strings.Split(s, ":")
	if len(splitted) != 2 {
		return domaintasks.IdempotencyRecord{}, false
	}

	id, err := uuid.Parse(splitted[0])
	if err != nil {
		return domaintasks.IdempotencyRecord{}, false
	}

	dec, err := hex.DecodeString(splitted[1])
	if err != nil || len(dec) != 16 {
		return domaintasks.IdempotencyRecord{}, false
	}

	var fp [16]byte
	copy(fp[:], dec)

	return domaintasks.IdempotencyRecord{TaskID: id, Fingerprint: fp}, true
}
