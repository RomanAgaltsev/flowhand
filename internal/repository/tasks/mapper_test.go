package tasks

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/storage/queries"
)

// fakeCatalog stands in for the worker registry on the write path.
type fakeCatalog struct{}

func (fakeCatalog) Has(string) bool { return true }

// submitted builds a pending task the way the Commander does.
func submitted(t *testing.T, id uuid.UUID, payload json.RawMessage, now time.Time) domaintasks.Task {
	t.Helper()
	handler, err := domaintasks.NewHandler("echo", fakeCatalog{})
	require.NoError(t, err)
	task, err := domaintasks.Submit(id, handler, 3, payload, 5, now)
	require.NoError(t, err)
	return task
}

func TestToDomain(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	key := "idem-1"
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	row := queries.Task{
		ID:             id,
		IdempotencyKey: &key,
		Payload:        []byte(`{"msg":"hi"}`),
		Status:         "running",
		CreatedAt:      createdAt,
		EarliestAt:     createdAt,
		Handler:        "echo",
		Priority:       3,
		MaxAttempts:    5,
	}

	task, err := toDomain(row, nil)
	require.NoError(t, err)

	assert.Equal(t, id, task.ID())
	assert.Equal(t, domaintasks.StatusRunning, task.Status())
	assert.Equal(t, "echo", task.Handler().Name())
	assert.Equal(t, domaintasks.Priority(3), task.Priority())
	assert.Equal(t, uint8(5), task.MaxAttempts())
	assert.Equal(t, json.RawMessage(`{"msg":"hi"}`), task.Payload())
	assert.True(t, createdAt.Equal(task.CreatedAt()))
	assert.Empty(t, task.Attempts())
}

func TestToRow(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	key := "idem-1"
	task := submitted(t, id, json.RawMessage(`{"msg":"hi"}`), time.Now())

	params := toRow(task, &key)

	assert.Equal(t, id, params.ID)
	assert.Equal(t, &key, params.IdempotencyKey)
	assert.Equal(t, "echo", params.Handler)
	assert.Equal(t, []byte(`{"msg":"hi"}`), params.Payload)
	// Before D2 these three were hardcoded in toRow because Task had nowhere
	// to keep them. Asserting them is what keeps them from drifting back.
	assert.Equal(t, int16(3), params.Priority)
	assert.Equal(t, int32(5), params.MaxAttempts)
	assert.Equal(t, int32(0), params.Attempt)
}

func TestToRowWithoutIdempotencyKey(t *testing.T) {
	task := submitted(t, uuid.Must(uuid.NewV7()), json.RawMessage(`{}`), time.Now())

	params := toRow(task, nil)

	assert.Nil(t, params.IdempotencyKey)
}

func TestMapperRoundTrip(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	want := submitted(t, id, json.RawMessage(`{"msg":"hi"}`), createdAt)

	params := toRow(want, nil)

	// Stands in for the RETURNING clause: the INSERT supplies id, payload and
	// handler; Postgres fills status and created_at from the 00001 defaults.
	row := queries.Task{
		ID:             params.ID,
		IdempotencyKey: params.IdempotencyKey,
		Payload:        params.Payload,
		Status:         string(domaintasks.StatusPending),
		CreatedAt:      createdAt,
		EarliestAt:     params.EarliestAt,
		Handler:        params.Handler,
		Priority:       params.Priority,
		MaxAttempts:    params.MaxAttempts,
	}

	got, err := toDomain(row, nil)
	require.NoError(t, err)

	assert.Equal(t, want.ID(), got.ID())
	assert.Equal(t, want.Status(), got.Status())
	assert.Equal(t, want.Handler(), got.Handler())
	assert.Equal(t, want.Priority(), got.Priority())
	assert.Equal(t, want.MaxAttempts(), got.MaxAttempts())
	assert.Equal(t, want.Payload(), got.Payload())
	assert.True(t, want.CreatedAt().Equal(got.CreatedAt()))
	assert.True(t, want.EarliestAt().Equal(got.EarliestAt()))
}

// Attempts are the half of the aggregate D2 added, and the type system will not
// catch a mapper that silently drops them. Two attempts, because one closed and
// one open is the shape that exercises both nullable-time branches.
func TestMapperRoundTripCarriesAttempts(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)

	task := submitted(t, id, json.RawMessage(`{}`), now)
	require.NoError(t, task.Start(uuid.Must(uuid.NewV7()), "worker-1", now.Add(time.Minute), now))
	require.NoError(t, task.ScheduleRetry("timeout", "deadline exceeded", now.Add(time.Minute), now))
	require.NoError(t, task.Start(uuid.Must(uuid.NewV7()), "worker-2", now.Add(2*time.Minute), now))

	rows := toAttemptRows(task)
	require.Len(t, rows, 2)
	assert.Equal(t, int32(1), rows[0].Attempt)
	assert.Equal(t, int32(2), rows[1].Attempt)
	assert.Equal(t, "worker-1", rows[0].WorkerID)
	// The first attempt closed as failed and carries its reason; the second is
	// still open, so finished_at is NULL and the error columns are NULL too.
	assert.Equal(t, string(domaintasks.AttemptFailed), rows[0].Status)
	require.NotNil(t, rows[0].FinishedAt)
	require.NotNil(t, rows[0].ErrorClass)
	assert.Equal(t, "timeout", *rows[0].ErrorClass)
	assert.Equal(t, string(domaintasks.AttemptRunning), rows[1].Status)
	assert.Nil(t, rows[1].FinishedAt)
	assert.Nil(t, rows[1].ErrorClass)

	// Back through toDomain via the attempt rows the INSERT would have written.
	attemptRows := make([]queries.TaskAttempt, 0, len(rows))
	for _, r := range rows {
		attemptRows = append(attemptRows, queries.TaskAttempt{
			ID: r.ID, TaskID: r.TaskID, Attempt: r.Attempt, WorkerID: r.WorkerID,
			Status: r.Status, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			LastHeartbeat: r.LastHeartbeat, ErrorClass: r.ErrorClass,
			ErrorMessage: r.ErrorMessage,
		})
	}

	params := toRow(task, nil)
	got, err := toDomain(queries.Task{
		ID: params.ID, Payload: params.Payload, Status: string(task.Status()),
		CreatedAt: now, EarliestAt: params.EarliestAt, Handler: params.Handler,
		Priority: params.Priority, MaxAttempts: params.MaxAttempts,
	}, attemptRows)
	require.NoError(t, err)

	require.Len(t, got.Attempts(), 2)
	assert.Equal(t, uint8(2), got.AttemptCount())
	assert.Equal(t, "worker-1", got.Attempts()[0].WorkerID())
	assert.Equal(t, "timeout", got.Attempts()[0].ErrorClass())
	assert.False(t, got.Attempts()[0].IsOpen())
	assert.True(t, got.Attempts()[1].IsOpen())

	// Reconstruction is silent: a FromPersistence that re-ran Submit's logic
	// would republish task.submitted.v1 on every GET.
	var replayed int
	for range got.PullEvents() {
		replayed++
	}
	assert.Zero(t, replayed)
}

// A row can hold a value the domain rejects — written before the CHECK
// existed, or by a later migration. Step 4's contract: the mapper reports
// corrupt data as an error instead of letting it become a panic three layers
// up.
//
// 256 and 512 are the cases that matter most: a bare uint8(row.Priority) wraps
// them to 0 and 0, which sails through NewPriority as an ordinary priority. A
// table of 200/-1 alone passes against that bug and proves nothing.
func TestToDomainRejectsCorruptPriority(t *testing.T) {
	for _, priority := range []int16{200, -1, 256, 512, 32767} {
		t.Run(strconv.Itoa(int(priority)), func(t *testing.T) {
			row := validRow()
			row.Priority = priority

			_, err := toDomain(row, nil)

			assert.ErrorIs(t, err, domaintasks.ErrInvalidPriority)
		})
	}
}

// The column is INT and the domain is uint16 (D8), so the read path is the only
// thing standing between a wider stored value and a silent truncation.
func TestToDomainRejectsCorruptShardID(t *testing.T) {
	for _, shard := range []int32{-1, 65536, math.MaxInt32} {
		t.Run(strconv.Itoa(int(shard)), func(t *testing.T) {
			row := validRow()
			row.ShardID = shard

			_, err := toDomain(row, nil)

			assert.ErrorIs(t, err, domaintasks.ErrInvalidShardID)
		})
	}
}

// lease_epoch is BIGINT (signed) but only ever increments from zero. A negative
// value converted straight to uint64 becomes an enormous epoch, against which
// every live lease would compare stale — so it is rejected, not wrapped.
func TestToDomainRejectsNegativeLeaseEpoch(t *testing.T) {
	row := validRow()
	epoch := int64(-1)
	row.LeaseEpoch = &epoch

	_, err := toDomain(row, nil)

	assert.ErrorIs(t, err, domaintasks.ErrInvalidLeaseEpoch)
}

// Round-trip: proves a value survives the trip to a row and back unchanged.
// The type system will not catch a mapper that drops or truncates a field.

func TestPriorityRoundTripsThroughTheRow(t *testing.T) {
	for p := uint8(0); p <= uint8(domaintasks.MaxPriority); p++ {
		got, err := priorityFromRow(int16(p))
		require.NoError(t, err)
		assert.Equal(t, domaintasks.Priority(p), got)
	}
}

func TestShardIDRoundTripsThroughTheRow(t *testing.T) {
	// 32768 is the smallint trap: it fits uint16 and the INT column, but not
	// the signed int16 that S0's notes once called for.
	for _, s := range []int32{0, 1, 32767, 32768, 65535} {
		got, err := shardIDFromRow(s)
		require.NoError(t, err)
		//nolint:gosec // G115: every s in the table is inside 0..65535.
		assert.Equal(t, domaintasks.ShardID(s), got)
	}

	_, err := shardIDFromRow(65536)
	assert.ErrorIs(t, err, domaintasks.ErrInvalidShardID)
}

func TestLeaseEpochRoundTripsThroughTheRow(t *testing.T) {
	// NULL means never dispatched. Zero is the right absence value: IsStaleVs
	// treats it as older than every stamped epoch, so an unleased row can never
	// fence out a live worker.
	got, err := leaseEpochFromRow(nil)
	require.NoError(t, err)
	assert.Equal(t, domaintasks.LeaseEpoch(0), got)

	for _, e := range []int64{0, 1, math.MaxInt64} {
		stored := e
		got, err := leaseEpochFromRow(&stored)
		require.NoError(t, err)
		//nolint:gosec // G115: e is non-negative by construction.
		assert.Equal(t, domaintasks.LeaseEpoch(uint64(e)), got)
	}
}

// validRow is a row every field of which the domain accepts, so a test can
// corrupt exactly one field and know that field is what the error is about.
func validRow() queries.Task {
	return queries.Task{
		ID:          uuid.Must(uuid.NewV7()),
		Status:      string(domaintasks.StatusPending),
		Handler:     "echo",
		Payload:     []byte(`{}`),
		CreatedAt:   time.Now().UTC(),
		Priority:    0,
		ShardID:     0,
		MaxAttempts: 1,
	}
}
