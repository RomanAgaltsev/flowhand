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
	ev := tasks.Submitted{
		TaskID:   uuid.MustParse("018f1e60-0000-7000-8000-000000000001"),
		Handler:  tasks.HandlerFromPersistence("echo"),
		Priority: 0,
		At:       at,
	}

	raw, err := encodeEnvelope(ev)
	require.NoError(t, err)

	want := `{"event":"task.submitted.v1","occurred_at":"2026-09-18T12:00:00Z",` +
		`"payload":{"TaskID":"018f1e60-0000-7000-8000-000000000001","Handler":"echo","Priority":0,"At":"2026-09-18T12:00:00Z"}}`
	assert.Equal(t, want, string(raw)) // BYTE comparison, not JSONEq
}

func TestEnvelopeRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	ev := tasks.Failed{
		TaskID:       uuid.MustParse("018f1e60-0000-7000-8000-000000000002"),
		Handler:      tasks.HandlerFromPersistence("echo"),
		Priority:     1,
		ErrorClass:   "timeout",
		ErrorMessage: "dep took too long",
		At:           at,
	}

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
