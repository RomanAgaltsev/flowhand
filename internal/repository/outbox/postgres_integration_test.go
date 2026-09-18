//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.opentelemetry.io/otel/trace"

	"github.com/RomanAgaltsev/flowhand/internal/config"
	"github.com/RomanAgaltsev/flowhand/internal/domain"
	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	repoutbox "github.com/RomanAgaltsev/flowhand/internal/repository/outbox"
	"github.com/RomanAgaltsev/flowhand/internal/storage"
	"github.com/RomanAgaltsev/flowhand/internal/storage/txmgr"
	"github.com/RomanAgaltsev/flowhand/migrations"
)

// startPostgresWithMigrations brings up a throwaway Postgres, applies every
// goose migration against it, and returns a pool pointed at it.
//
// A deliberate copy of the tasks package's harness rather than a shared
// testutil helper: the two suites must stay independently runnable, and the
// harness is small enough that sharing it would couple the packages the task
// just split apart.
func startPostgresWithMigrations(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pg, err := postgres.Run(
		ctx, "postgres:18-alpine",
		postgres.WithDatabase("flowhand"),
		postgres.WithUsername("flowhand"),
		postgres.WithPassword("flowhand"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, pg)

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := storage.NewPool(ctx, config.DB{DSN: dsn, MaxConns: 4, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	p, closeProvider, err := storage.NewGooseProvider(
		ctx,
		config.DB{DSN: dsn, MaxConns: 2, MinConns: 1},
		migrations.FS,
	)
	require.NoError(t, err)
	defer func() { require.NoError(t, closeProvider()) }()

	_, err = p.Up(ctx)
	require.NoError(t, err, "migrations must apply cleanly")

	return pool
}

// fakeCatalog stands in for the worker registry; Append never consults it, but
// building a Task through the validating constructors does.
type fakeCatalog struct{}

func (fakeCatalog) Has(string) bool { return true }

// submitted builds a pending task through the validating constructors. The
// task is never inserted — outbox rows reference the aggregate by id only.
func submitted(t *testing.T) domaintasks.Task {
	t.Helper()
	handler, err := domaintasks.NewHandler("echo", fakeCatalog{})
	require.NoError(t, err)
	task, err := domaintasks.Submit(
		uuid.Must(uuid.NewV7()), handler, 0, json.RawMessage(`{"msg":"hi"}`), 25,
		time.Now().UTC().Truncate(time.Microsecond),
	)
	require.NoError(t, err)
	return task
}

// TestEnvelopeShape appends two events and reads the rows back through the
// pool, proving every outbox_events column lands what Append claims to write:
// aggregate_id (the WHERE finds exactly these rows), type, envelope_ver=1,
// trace_id NULL without a span and the span's trace id with one, sent=false,
// and a payload that decodes to the documented envelope.
//
// The payload comparison is semantic (JSONEq + field asserts), not
// byte-identical: JSONB rewrites key order and whitespace on storage, so the
// column can never return encodeEnvelope's exact bytes. The byte shape itself
// is pinned by TestEncodeEnvelope_ExactBytes in mapper_test.go.
func TestEnvelopeShape(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	repo := repoutbox.New(txmgr.New(pool))
	ctx := context.Background()

	tt := submitted(t)
	submittedAt := time.Now().UTC().Truncate(time.Microsecond)
	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	evSubmitted := domaintasks.NewSubmitted(tt, submittedAt)
	evStarted := domaintasks.NewStarted(tt, startedAt)

	// No span on ctx: the row's trace_id must be NULL.
	require.NoError(t, repo.Append(ctx, evSubmitted))

	// A valid span on ctx: the row's trace_id must be its trace id.
	tid, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	sid, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	spanCtx := trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	}))
	require.NoError(t, repo.Append(spanCtx, evStarted))

	rows, err := pool.Query(ctx, `
		SELECT type, envelope_ver, trace_id, payload, sent
		FROM outbox_events
		WHERE aggregate_id = $1
		ORDER BY type`, tt.ID())
	require.NoError(t, err)
	defer rows.Close()

	type outboxRow struct {
		Type        string
		EnvelopeVer int16
		TraceID     *string
		Payload     []byte
		Sent        bool
	}
	var got []outboxRow
	for rows.Next() {
		var r outboxRow
		require.NoError(t, rows.Scan(&r.Type, &r.EnvelopeVer, &r.TraceID, &r.Payload, &r.Sent))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())

	// Exactly two rows for this aggregate_id — no more, no less — which is
	// also the assertion that aggregate_id landed correctly.
	require.Len(t, got, 2, "one row per appended event, keyed by aggregate_id")

	// ORDER BY type: 'task.started.v1' sorts before 'task.submitted.v1'.
	startedRow, submittedRow := got[0], got[1]

	assert.Equal(t, "task.started.v1", startedRow.Type)
	assert.Equal(t, int16(domain.EnvelopeVersion), startedRow.EnvelopeVer)
	require.NotNil(t, startedRow.TraceID, "a span on ctx must land in trace_id")
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", *startedRow.TraceID)
	assert.False(t, startedRow.Sent, "fresh rows are unsent — the relay's worklist")
	assertEnvelope(t, evStarted, startedRow.Payload)

	assert.Equal(t, "task.submitted.v1", submittedRow.Type)
	assert.Equal(t, int16(domain.EnvelopeVersion), submittedRow.EnvelopeVer)
	assert.Nil(t, submittedRow.TraceID, "no span on ctx must leave trace_id NULL")
	assert.False(t, submittedRow.Sent)
	assertEnvelope(t, evSubmitted, submittedRow.Payload)
}

// assertEnvelope checks a payload column against the event it was written
// from: the three documented envelope fields, nothing more.
func assertEnvelope(t *testing.T, ev domain.Event, payload []byte) {
	t.Helper()
	var env struct {
		Event      string          `json:"event"`
		OccurredAt time.Time       `json:"occurred_at"`
		Payload    json.RawMessage `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(payload, &env), "payload must decode as the envelope")

	assert.Equal(t, ev.EventName(), env.Event)
	assert.True(t, ev.OccurredAt().Equal(env.OccurredAt),
		"occurred_at: wrote %s, read back %s", ev.OccurredAt(), env.OccurredAt)

	wantPayload, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantPayload), string(env.Payload),
		"the event's own JSON must ride inside the envelope unharmed")
}
