package tasks

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// Priority orders the dequeue scan: higher values run sooner.
// A defined type over uint8 rather than a struct - a caller CAN write Priority(200)
// and bypass NewPriority. That hole is accepted here because the ergonomics of a
// plain comparable number matter more than the invariant. The backstop is
// tasks_priority_check (migrations/00004_shard_priority_checks.sql).
type Priority uint8

// MaxPriority is the highest priority a task may carry. schema.md: higher = sooner.
const MaxPriority Priority = 9

// NewPriority creates new Priority.
func NewPriority(p uint8) (Priority, error) {
	if p > uint8(MaxPriority) {
		return 0, fmt.Errorf("%w: %d exceeds %d", ErrInvalidPriority, p, MaxPriority)
	}
	return Priority(p), nil
}

// IsHigherThan reports whether p runs sooner than other. Higher values run
// sooner (schema.md), which is exactly the fact this method exists to hide.
// Strict: a tie is not "higher".
func (p Priority) IsHigherThan(other Priority) bool { return p > other }

// IdempotencyKey is a struct, not a defined string type, so that the zero value is
// unmistakably invalid and there is no conversion that bypasses NewIdempotencyKey.
type IdempotencyKey struct{ s string }

// MaxIdempotencyKeyBytes bounds the key at the width of the idempotency_key
// column. Keys are ASCII-only, so bytes and runes coincide.
const MaxIdempotencyKeyBytes = 255

// NewIdempotencyKey creates new IdempotencyKey.
func NewIdempotencyKey(s string) (IdempotencyKey, error) {
	if s == "" {
		return IdempotencyKey{}, ErrIdempotencyKeyEmpty
	}
	// ASCII BEFORE length: "255" is ambiguous between bytes and runes for any
	// non-ASCII string, and this check is what settles it. Reversed, a 200-rune
	// 600-byte key would be reported as too long rather than as non-ASCII.
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return IdempotencyKey{}, fmt.Errorf("%w at offset %d", ErrIdempotencyKeyNotASCII, i)
		}
	}
	if len(s) > MaxIdempotencyKeyBytes {
		return IdempotencyKey{}, fmt.Errorf("%w: %d bytes", ErrIdempotencyKeyTooLong, len(s))
	}
	return IdempotencyKey{s: s}, nil
}

// String returns IdempotencyKey string representation.
func (k IdempotencyKey) String() string {
	return k.s
}

// Hash is the Redis fast-path lookup key and the payload_hash comparison basis.
func (k IdempotencyKey) Hash() [32]byte {
	return sha256.Sum256([]byte(k.s))
}

// Handler is a validated handler name.
type Handler struct {
	name string
}

// NewHandler constructs a Handler. Construction requires the catalog, which means
// a Handler value is proof that the name was registered AT THE TIME IT WAS BUILT.
func NewHandler(name string, cat HandlerCatalog) (Handler, error) {
	if name == "" {
		return Handler{}, ErrEmptyHandler
	}
	if cat == nil {
		return Handler{}, fmt.Errorf("%w: nil catalog for %q", ErrNilHandlerCatalog, name)
	}
	if !cat.Has(name) {
		return Handler{}, fmt.Errorf("%w: %q", ErrUnknownHandler, name)
	}
	return Handler{name: name}, nil
}

// Name returns Handler's name as a string.
func (h Handler) Name() string {
	return h.name
}

// MarshalJSON publishes the handler as its bare name - events are a published
// format (outbox payload -> Kafka) and "{}" would lose the name entirely.
func (h Handler) MarshalJSON() ([]byte, error) { return json.Marshal(h.name) }

// UnmarshalJSON is MarshalJSON's inverse, for consumers rebuilding events.
func (h *Handler) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	h.name = s
	return nil
}

// HandlerCatalog is declared HERE, in the domain, not in the worker package that
// implements it. That direction is the entire point: the domain states what it needs
// ("something that can tell me whether a handler name is registered") and the
// composition root supplies it. The domain imports nothing to get this.
//
// Accept interfaces, return structs - and declare the interface at the CONSUMER.
type HandlerCatalog interface {
	Has(name string) bool
}

// LeaseEpoch is the shard-ownership fencing token stamped onto a row at
// dispatch (schema.md, "The lease-epoch fencing mechanism"; ADR 0004).
type LeaseEpoch uint64

// IsStaleVs reports whether e is older than other — the fencing rejection
// test. The epoch compared is the one stamped AT DISPATCH, never the current
// epoch: the global rebalance epoch bumps on every membership change, so an
// epoch-now fence would zero-row every in-flight completion after a scale event.
func (e LeaseEpoch) IsStaleVs(other LeaseEpoch) bool { return e < other }

// ShardID is a consistent-hash partition of the task space (schema.md:
// shard_id INT, domain range 0..65535). No constructor: every uint16 is a
// valid shard, and the mapper bounds-checks the wider column on read.
type ShardID uint16

// Bucket folds a shard id into one of n low-cardinality buckets for use as a
// metric label — never label a series with the raw shard id
// (design/distributed/cardinality.md: 8 buckets, shard_id mod 8).
//
// n must be positive; n <= 0 returns 0 rather than panicking, because this
// runs on an instrumentation path where a misconfigured bucket count must not
// take down the caller.
func (s ShardID) Bucket(n int) int {
	if n <= 0 {
		return 0
	}
	return int(s) % n
}
