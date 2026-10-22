package outbox

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
)

func TestEncodeEnvelope_ExactBytes(t *testing.T) {
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	task, err := tasks.Submit(uuid.MustParse("018f1e60-0000-7000-8000-000000000001"),
		tasks.HandlerFromPersistence("echo"), 0, json.RawMessage(`{}`), 5, at)
	require.NoError(t, err)
	ev := tasks.NewSubmitted(task, at)

	raw, err := encodeEnvelope(ev)
	require.NoError(t, err)

	want := `{"event":"task.submitted.v1","occurred_at":"2026-09-18T12:00:00Z",` +
		`"payload":{"task_id":"018f1e60-0000-7000-8000-000000000001","handler":"echo","priority":0,"at":"2026-09-18T12:00:00Z"}}`
	assert.Equal(t, want, string(raw)) // BYTE comparison, not JSONEq
}

func TestEnvelopeRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	task, err := tasks.Submit(uuid.MustParse("018f1e60-0000-7000-8000-000000000002"),
		tasks.HandlerFromPersistence("echo"), 1, json.RawMessage(`{}`), 5, at)
	require.NoError(t, err)
	ev := tasks.NewFailed(task, "timeout", "dep took too long", at)

	raw, err := encodeEnvelope(ev)
	require.NoError(t, err)

	got, err := decodeEnvelope(raw)
	require.NoError(t, err)
	assert.Equal(t, ev.EventName(), got.EventName)
	assert.True(t, ev.OccurredAt().Equal(got.OccurredAt))

	wantPayload, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantPayload), string(got.Payload))
}
