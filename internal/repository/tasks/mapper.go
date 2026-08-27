package tasks

import (
	"fmt"
	"math"

	"github.com/google/uuid"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
)

// toDomain converts a sqlc row. This is a fallible conversion: the database can
// hold a value the domain rejects (a row written before the CHECK existed, or by
// a migration). Returning an error here keeps that from becoming a panic three
// layers up.
//
// The converted priority/shard/epoch are validated and discarded until D2 widens
// the Task aggregate to carry them. Validating now is deliberate: it is the point
// at which corrupt data is caught, and D2 only has to add the fields.
func toDomain(row queries.Task) (domaintasks.Task, error) {
	if _, err := priorityFromRow(row.Priority); err != nil {
		return domaintasks.Task{}, fmt.Errorf("task %s: %w", row.ID, err)
	}
	if _, err := shardIDFromRow(row.ShardID); err != nil {
		return domaintasks.Task{}, fmt.Errorf("task %s: %w", row.ID, err)
	}
	if _, err := leaseEpochFromRow(row.LeaseEpoch); err != nil {
		return domaintasks.Task{}, fmt.Errorf("task %s: %w", row.ID, err)
	}

	return domaintasks.FromPersistence(
		row.ID,
		domaintasks.Status(row.Status),
		// Stays a bare string until D2 widens Task to carry a Handler. The read
		// path will then use domaintasks.HandlerFromPersistence, which skips the
		// catalog on purpose — see its doc comment.
		row.Handler,
		row.Payload,
		row.CreatedAt,
	), nil
}

// priorityFromRow narrows the SMALLINT column to the domain's uint8.
//
// The narrowing is guarded, not assumed: a bare uint8(row.Priority) WRAPS, so a
// stored 256 would arrive as 0 and pass validation as a perfectly ordinary
// priority. Guarding the conversion is the whole point of the function.
func priorityFromRow(v int16) (domaintasks.Priority, error) {
	if v < 0 || v > math.MaxUint8 {
		return 0, fmt.Errorf("%w: priority %d outside uint8", domaintasks.ErrInvalidPriority, v)
	}
	return domaintasks.NewPriority(uint8(v)) //nolint:gosec // G115: bounds-checked above.
}

// leaseEpochFromRow reads the nullable fencing token.
//
// NULL means the row has never been dispatched. Zero is the right absence
// value: IsStaleVs treats it as older than every stamped epoch, so an unleased
// row can never fence out a live worker.
//
// A negative epoch is corrupt — BIGINT is signed but the counter only ever
// increments from zero — and is reported rather than wrapped to a huge uint64,
// which would make every live lease look stale.
func leaseEpochFromRow(e *int64) (domaintasks.LeaseEpoch, error) {
	if e == nil {
		return 0, nil
	}
	if *e < 0 {
		return 0, fmt.Errorf("%w: lease_epoch %d is negative", domaintasks.ErrInvalidLeaseEpoch, *e)
	}
	return domaintasks.LeaseEpoch(*e), nil //nolint:gosec // G115: bounds-checked above.
}

// shardIDFromRow narrows the INT column to the domain's uint16 (D8: the column
// is deliberately wider than the domain range, so the read path bounds-checks).
func shardIDFromRow(v int32) (domaintasks.ShardID, error) {
	if v < 0 || v > math.MaxUint16 {
		return 0, fmt.Errorf("%w: shard_id %d outside 0..%d",
			domaintasks.ErrInvalidShardID, v, math.MaxUint16)
	}
	return domaintasks.ShardID(v), nil // #nosec G115 -- bounds-checked above
}

var defaultTenantID = uuid.Nil

func toRow(t domaintasks.Task, idempotencyKey *string) queries.CreateTaskParams {
	return queries.CreateTaskParams{
		ID:              t.ID(),
		IdempotencyKey:  idempotencyKey,
		Payload:         t.Payload(),
		Handler:         t.Handler(),
		TenantID:        defaultTenantID,
		Priority:        0,
		EarliestAt:      t.CreatedAt(),
		Attempt:         0,
		MaxAttempts:     25, // schema.md: per-HandlerSpec default
		ShardID:         0,
		CancelRequested: false,
		UpdatedAt:       t.CreatedAt(),
	}
}
