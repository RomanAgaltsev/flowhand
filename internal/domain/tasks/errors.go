package tasks

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned by repositories when no task matches the lookup.
// Defined here, not in internal/repository, so internal/api can test for it
// without importing the repository layer.
var ErrNotFound = errors.New("task not found")

// ErrConflict is returned when an insert collides with the partial unique
// index on idempotency_key. The service layer turns it into a replay.
var ErrConflict = errors.New("idempotency key conflict")

// ErrInvalidPriority is returned when a given priority is higher than
// MaxPriority.
var ErrInvalidPriority = errors.New("priority out of range")

// ErrInvalidIdempotencyKey is the umbrella sentinel; the three below wrap it,
// so errors.Is against either the umbrella or the specific reason works.
var ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")

var (
	// ErrIdempotencyKeyEmpty is returned when the key is the empty string.
	ErrIdempotencyKeyEmpty = fmt.Errorf("%w: empty", ErrInvalidIdempotencyKey)

	// ErrIdempotencyKeyNotASCII is returned when the key holds a byte outside
	// ASCII. Checked before the length bound, so a long non-ASCII key reports
	// this rather than the misleading "too long".
	ErrIdempotencyKeyNotASCII = fmt.Errorf("%w: contains a non-ASCII byte", ErrInvalidIdempotencyKey)

	// ErrIdempotencyKeyTooLong is returned when the key exceeds
	// MaxIdempotencyKeyBytes. Keys are ASCII, so bytes and runes coincide.
	ErrIdempotencyKeyTooLong = fmt.Errorf("%w: longer than %d bytes", ErrInvalidIdempotencyKey, MaxIdempotencyKeyBytes)
)

// ErrInvalidShardID is returned when a stored shard_id is outside the uint16
// range the domain represents. See D8.
var ErrInvalidShardID = errors.New("shard id out of range")

// ErrInvalidLeaseEpoch is returned when a stored lease_epoch is negative. The
// column is BIGINT (signed) but the counter only ever increments from zero, so
// a negative value is corrupt data rather than a stale lease.
var ErrInvalidLeaseEpoch = errors.New("lease epoch out of range")

// ErrEmptyHandler is returned when a handler name is the empty string.
var ErrEmptyHandler = errors.New("empty handler name")

// ErrUnknownHandler is returned when the handler catalog does not know the
// name. Defined here, at the consumer of the catalog port, so callers can
// branch on it without importing whatever implements the port.
var ErrUnknownHandler = errors.New("unknown handler")

// ErrNilHandlerCatalog is returned when NewHandler is called without a catalog.
// This is a wiring bug in the composition root, not bad user input — but it is
// returned rather than panicked so no request path can take down the process.
var ErrNilHandlerCatalog = errors.New("nil handler catalog")
