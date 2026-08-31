package tasks

import (
	"fmt"
	"math"

	"github.com/google/uuid"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
)

// toDomain converts a sqlc row plus that task's attempt rows back into the
// aggregate. This is a fallible conversion: the database can hold a value the
// domain rejects (a row written before the CHECK existed, or by a migration).
// Returning an error here keeps that from becoming a panic three layers up.
//
// It goes through domaintasks.FromPersistence, which assigns fields directly
// and emits NO events. Running Submit's logic here would re-emit
// task.submitted.v1 on every read and the relay would publish a duplicate
// submission for every GET.
func toDomain(row queries.Task, attemptRows []queries.TaskAttempt) (domaintasks.Task, error) {
	priority, err := priorityFromRow(row.Priority)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("task %s: %w", row.ID, err)
	}
	// Validated and discarded: the shard is a routing property of the row, not
	// of the aggregate, so Task carries no shard field. The check still earns
	// its place - this is the point at which a corrupt value is caught.
	if _, err := shardIDFromRow(row.ShardID); err != nil {
		return domaintasks.Task{}, fmt.Errorf("task %s: %w", row.ID, err)
	}
	leaseEpoch, err := leaseEpochFromRow(row.LeaseEpoch)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("task %s: %w", row.ID, err)
	}
	maxAttempts, err := maxAttemptsFromRow(row.MaxAttempts)
	if err != nil {
		return domaintasks.Task{}, fmt.Errorf("task %s: %w", row.ID, err)
	}

	attempts := make([]domaintasks.Attempt, 0, len(attemptRows))
	for _, a := range attemptRows {
		attempts = append(attempts, attemptFromRow(a))
	}

	return domaintasks.FromPersistence(
		row.ID,
		domaintasks.Status(row.Status),
		// HandlerFromPersistence skips the catalog on purpose - see its doc
		// comment. A row went through NewHandler on the way in, so re-checking
		// would mean threading a HandlerCatalog into the repository to
		// re-validate data we wrote ourselves.
		domaintasks.HandlerFromPersistence(row.Handler),
		priority,
		row.Payload,
		attempts,
		leaseEpoch,
		row.EarliestAt,
		maxAttempts,
		row.CreatedAt,
	), nil
}

// attemptFromRow rebuilds one attempt. Nullable text columns collapse to the
// empty string: the domain models "no error" as empty rather than as a pointer,
// because an attempt that succeeded has no error to distinguish from an absent
// one.
func attemptFromRow(a queries.TaskAttempt) domaintasks.Attempt {
	return domaintasks.AttemptFromPersistence(
		a.ID,
		domaintasks.AttemptStatus(a.Status),
		a.WorkerID,
		a.StartedAt,
		a.FinishedAt,
		a.LastHeartbeat,
		deref(a.ErrorClass),
		deref(a.ErrorMessage),
	)
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

// maxAttemptsFromRow narrows the INT column to the domain's uint8.
//
// Zero is rejected rather than carried: CanRetry would be false forever, so the
// task could never be retried and never cleanly dead-lettered - the same
// invariant Submit enforces on the write path, applied to stored data.
func maxAttemptsFromRow(v int32) (uint8, error) {
	if v <= 0 || v > math.MaxUint8 {
		return 0, fmt.Errorf("%w: max_attempts %d outside 1..%d",
			domaintasks.ErrInvalidMaxAttempts, v, math.MaxUint8)
	}
	return uint8(v), nil //nolint:gosec // G115: bounds-checked above.
}

// leaseEpochFromRow reads the nullable fencing token.
//
// NULL means the row has never been dispatched. Zero is the right absence
// value: IsStaleVs treats it as older than every stamped epoch, so an unleased
// row can never fence out a live worker.
//
// A negative epoch is corrupt - BIGINT is signed but the counter only ever
// increments from zero - and is reported rather than wrapped to a huge uint64,
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

// toRow projects the aggregate onto the tasks row. Every value now comes from
// the aggregate: before D2 the priority, attempt budget and earliest_at were
// hardcoded here because Task had nowhere to keep them.
func toRow(t domaintasks.Task, idempotencyKey *string) queries.CreateTaskParams {
	return queries.CreateTaskParams{
		ID:             t.ID(),
		IdempotencyKey: idempotencyKey,
		Payload:        t.Payload(),
		Handler:        t.Handler().Name(),
		TenantID:       defaultTenantID,
		Priority:       int16(t.Priority()),
		EarliestAt:     t.EarliestAt(),
		Attempt:        int32(t.AttemptCount()),
		MaxAttempts:    int32(t.MaxAttempts()),
		// Shard assignment is the scheduler's job (S0/CE3); the aggregate
		// carries no shard, so a fresh row lands on shard 0 until dispatch.
		ShardID:         0,
		CancelRequested: false,
		UpdatedAt:       t.CreatedAt(),
	}
}

// toAttemptRows projects the aggregate's attempt history onto task_attempts.
//
// The attempt NUMBER is the 1-based position in the slice rather than a field
// on Attempt: the aggregate already derives AttemptCount from the slice, and a
// stored number would be a second source of truth for the same fact.
func toAttemptRows(t domaintasks.Task) []queries.CreateAttemptParams {
	attempts := t.Attempts()
	rows := make([]queries.CreateAttemptParams, 0, len(attempts))
	for i, a := range attempts {
		rows = append(rows, queries.CreateAttemptParams{
			ID:            a.ID(),
			TaskID:        t.ID(),
			Attempt:       int32(i + 1),
			WorkerID:      a.WorkerID(),
			Status:        string(a.Status()),
			StartedAt:     a.StartedAt(),
			FinishedAt:    a.FinishedAt(),
			LastHeartbeat: a.LastHeartbeat(),
			ErrorClass:    ref(a.ErrorClass()),
			ErrorMessage:  ref(a.ErrorMessage()),
		})
	}
	return rows
}

// deref collapses a nullable text column to the empty string.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ref is deref's inverse: the empty string means "no value", which is stored as
// NULL so the column never holds an empty-string-vs-NULL distinction the domain
// cannot represent.
func ref(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
