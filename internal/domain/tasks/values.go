package tasks

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"
)

// Priority orders the dequeue scan: higher values run sooner.
// A defined type over uint8 rather than a struct - a caller CAN write Priority(200)
// and bypass NewPriority. That hole is accepted here because the ergonomics of a
// plain comparable number matter more than the invariant. The backstop is
// tasks_priority_check (migrations/00004_shard_priority_checks.sql).
type Priority uint8

// MaxPriority is the highest priority a task may carry. Higher runs sooner: the
// dequeue index orders priority DESC.
const MaxPriority Priority = 9

// NewPriority creates new Priority.
func NewPriority(p uint8) (Priority, error) {
	if p > uint8(MaxPriority) {
		return 0, fmt.Errorf("%w: %d exceeds %d", ErrInvalidPriority, p, MaxPriority)
	}
	return Priority(p), nil
}

// IsHigherThan reports whether p runs sooner than other. Higher values run
// sooner, which is exactly the fact this method exists to hide.
// Strict: a tie is not "higher".
func (p Priority) IsHigherThan(other Priority) bool { return p > other }

// IdempotencyKey is a struct, not a defined string type, so that the zero value is
// unmistakably invalid and there is no conversion that bypasses NewIdempotencyKey.
type IdempotencyKey struct{ s string }

// MaxIdempotencyKeyBytes matches the wire's maxLength. The TEXT column is
// unbounded, so this constant is the only bound for callers that are not the HTTP
// API. Keys are ASCII-only, so bytes and runes coincide.
const MaxIdempotencyKeyBytes = 128

// NewIdempotencyKey creates new IdempotencyKey.
func NewIdempotencyKey(s string) (IdempotencyKey, error) {
	if s == "" {
		return IdempotencyKey{}, ErrIdempotencyKeyEmpty
	}
	// ASCII BEFORE length: the bound is ambiguous between bytes and runes for any
	// non-ASCII string, and this check is what settles it. Reversed, a 100-rune
	// 300-byte key would be reported as too long rather than as non-ASCII.
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= utf8.RuneSelf:
			return IdempotencyKey{}, fmt.Errorf("%w at offset %d", ErrIdempotencyKeyNotASCII, i)
		case c < 0x20 || c == 0x7F:
			return IdempotencyKey{}, fmt.Errorf("%w at offset %d", ErrIdempotencyKeyNotPrintable, i)
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

// LogValue logs the key as its bare string. Without it the JSON handler marshals
// a struct with no exported fields, and the key logs as {}.
func (k IdempotencyKey) LogValue() slog.Value {
	return slog.StringValue(k.s)
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
	if !utf8.ValidString(name) || strings.IndexByte(name, 0) >= 0 {
		return Handler{}, fmt.Errorf("%w: %q", ErrInvalidHandlerName, name)
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
// dispatch.
type LeaseEpoch uint64

// IsStaleVs reports whether e is older than other — the fencing rejection
// test. The epoch compared is the one stamped AT DISPATCH, never the current
// epoch: the global rebalance epoch bumps on every membership change, so an
// epoch-now fence would zero-row every in-flight completion after a scale event.
func (e LeaseEpoch) IsStaleVs(other LeaseEpoch) bool { return e < other }

// ShardID is a consistent-hash partition of the task space (column shard_id INT,
// domain range 0..65535). No constructor: every uint16 is a
// valid shard, and the mapper bounds-checks the wider column on read.
type ShardID uint16

// Bucket folds a shard id into one of n low-cardinality buckets for use as a
// metric label — never label a series with the raw shard id, whose cardinality is
// unbounded for a dashboard. The convention is 8 buckets: shard_id mod 8.
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
