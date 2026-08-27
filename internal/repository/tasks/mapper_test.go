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
		Handler:        "echo",
	}

	task, err := toDomain(row)
	require.NoError(t, err)

	assert.Equal(t, id, task.ID())
	assert.Equal(t, domaintasks.StatusRunning, task.Status())
	assert.Equal(t, "echo", task.Handler())
	assert.Equal(t, json.RawMessage(`{"msg":"hi"}`), task.Payload())
	assert.True(t, createdAt.Equal(task.CreatedAt()))
}

func TestToRow(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	key := "idem-1"
	task := domaintasks.Submit(id, "echo", json.RawMessage(`{"msg":"hi"}`), time.Now())

	params := toRow(task, &key)

	assert.Equal(t, id, params.ID)
	assert.Equal(t, &key, params.IdempotencyKey)
	assert.Equal(t, "echo", params.Handler)
	assert.Equal(t, []byte(`{"msg":"hi"}`), params.Payload)
}

func TestToRowWithoutIdempotencyKey(t *testing.T) {
	task := domaintasks.Submit(uuid.Must(uuid.NewV7()), "echo", json.RawMessage(`{}`), time.Now())

	params := toRow(task, nil)

	assert.Nil(t, params.IdempotencyKey)
}

func TestMapperRoundTrip(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	want := domaintasks.Submit(id, "echo", json.RawMessage(`{"msg":"hi"}`), createdAt)

	params := toRow(want, nil)

	// Stands in for the RETURNING clause: the INSERT supplies id, payload and
	// handler; Postgres fills status and created_at from the 00001 defaults.
	row := queries.Task{
		ID:             params.ID,
		IdempotencyKey: params.IdempotencyKey,
		Payload:        params.Payload,
		Status:         string(domaintasks.StatusPending),
		CreatedAt:      createdAt,
		Handler:        params.Handler,
	}

	got, err := toDomain(row)
	require.NoError(t, err)

	assert.Equal(t, want.ID(), got.ID())
	assert.Equal(t, want.Status(), got.Status())
	assert.Equal(t, want.Handler(), got.Handler())
	assert.Equal(t, want.Payload(), got.Payload())
	assert.True(t, want.CreatedAt().Equal(got.CreatedAt()))
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

			_, err := toDomain(row)

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

			_, err := toDomain(row)

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

	_, err := toDomain(row)

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
		ID:        uuid.Must(uuid.NewV7()),
		Status:    string(domaintasks.StatusPending),
		Handler:   "echo",
		Payload:   []byte(`{}`),
		CreatedAt: time.Now().UTC(),
		Priority:  0,
		ShardID:   0,
	}
}
