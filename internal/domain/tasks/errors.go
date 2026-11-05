package tasks

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned by repositories when no task matches the lookup.
// Defined here, not in internal/repository, so internal/api can test for it
// without importing the repository layer.
var ErrNotFound = errors.New("task not found")

// ErrConflict is returned when an insert collides with the idempotency
// ledger's primary key. The service layer turns it into a replay.
var ErrConflict = errors.New("idempotency key conflict")

// ErrInvalidPriority is returned when a given priority is higher than
// MaxPriority.
var ErrInvalidPriority = errors.New("priority out of range")

// ErrInvalidIdempotencyKey is the umbrella sentinel; the four below wrap it,
// so errors.Is against either the umbrella or the specific reason works.
var ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")

// ErrIdempotencyKeyEmpty is returned when the key is the empty string.
var ErrIdempotencyKeyEmpty = fmt.Errorf("%w: empty", ErrInvalidIdempotencyKey)

// ErrIdempotencyKeyNotASCII is returned when the key holds a byte outside
// ASCII. Checked before the length bound, so a long non-ASCII key reports
// this rather than the misleading "too long".
var ErrIdempotencyKeyNotASCII = fmt.Errorf("%w: contains a non-ASCII byte", ErrInvalidIdempotencyKey)

// ErrIdempotencyKeyTooLong is returned when the key exceeds
// MaxIdempotencyKeyBytes. Keys are ASCII, so bytes and runes coincide.
var ErrIdempotencyKeyTooLong = fmt.Errorf("%w: longer than %d bytes", ErrInvalidIdempotencyKey, MaxIdempotencyKeyBytes)

// ErrIdempotencyKeyNotPrintable is returned when the key holds a byte outside
// printable ASCII (0x20-0x7E). NUL in particular cannot be stored in a
// Postgres TEXT column at all.
var ErrIdempotencyKeyNotPrintable = fmt.Errorf("%w: contains a non-printable byte", ErrInvalidIdempotencyKey)

// ErrInvalidShardID is returned when a stored shard_id is outside the uint16
// range the domain represents. The column is deliberately wider than the domain
// type, so the read path is where a bad value is caught.
var ErrInvalidShardID = errors.New("shard id out of range")

// ErrInvalidLeaseEpoch is returned when a stored lease_epoch is negative. The
// column is BIGINT (signed) but the counter only ever increments from zero, so
// a negative value is corrupt data rather than a stale lease.
var ErrInvalidLeaseEpoch = errors.New("lease epoch out of range")

// ErrEmptyHandler is returned when a handler name is the empty string.
var ErrEmptyHandler = errors.New("empty handler name")

// ErrInvalidHandlerName is returned when a handler name is not text a Postgres
// TEXT column can hold: invalid UTF-8, or U+0000. Checked before the catalog,
// because a permissive catalog must not be what stands between a client and a
// 500.
var ErrInvalidHandlerName = errors.New("invalid handler name")

// ErrUnknownHandler is returned when the handler catalog does not know the
// name. Defined here, at the consumer of the catalog port, so callers can
// branch on it without importing whatever implements the port.
var ErrUnknownHandler = errors.New("unknown handler")

// ErrNilHandlerCatalog is returned when NewHandler is called without a catalog.
// This is a wiring bug in the composition root, not bad user input — but it is
// returned rather than panicked so no request path can take down the process.
var ErrNilHandlerCatalog = errors.New("nil handler catalog")

// ErrInvalidMaxAttempts is returned when a task is submitted with a zero
// attempt budget. Zero is not "unlimited" — CanRetry would be false forever,
// so the task could never be retried and never cleanly dead-lettered.
var ErrInvalidMaxAttempts = errors.New("max attempts must be greater than zero")

// ErrNilPayload is returned when a task is submitted without a payload. An
// empty JSON object is a legitimate payload; a nil one is a caller bug.
var ErrNilPayload = errors.New("nil payload")

// ErrInvalidPayload is returned when a payload is not exactly one JSON value the
// task store can hold. The HTTP path produces it too: ogen decodes only the top
// level of the opaque payload, so a nested duplicate name, U+0000 or an
// out-of-range number all arrive here.
var ErrInvalidPayload = errors.New("invalid payload")

// ErrInvalidFingerprint is returned when a stored payload fingerprint is not
// exactly PayloadFingerprint's width: corrupt data, or a row written before the
// fingerprint was canonical. Never a fingerprint to compare against.
var ErrInvalidFingerprint = errors.New("payload fingerprint has the wrong length")
