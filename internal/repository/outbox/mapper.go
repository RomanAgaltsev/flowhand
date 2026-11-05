package outbox

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
)

// envelope wraps an event for the outbox_events.payload column. This is a
// PUBLISHED FORMAT: once the relay ships it into Kafka, changing field order or naming
// means a coordinated consumer migration.
//
// No "v" field: envelope_ver lives in the outbox_events COLUMN and travels as
// a Kafka header. Repeating it inside the payload is a second copy that
// can disagree — and the useless copy, since reading it costs the parse the
// header exists to avoid.
type envelope struct {
	EventName  string          `json:"event"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload"`
}

// encodeEnvelope marshals the event once and embeds those bytes. Payload MUST
// be json.RawMessage: assigning a marshaled string instead would produce a
// JSON string full of escaped JSON - decodes fine, wrong in a way no type
// check catches.
func encodeEnvelope(ev domain.Event) (json.RawMessage, error) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return json.Marshal(envelope{
		EventName:  ev.EventName(),
		OccurredAt: ev.OccurredAt(),
		Payload:    payload,
	})
}

// decodeEnvelope is encodeEnvelope's inverse.
func decodeEnvelope(raw json.RawMessage) (envelope, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	return env, nil
}
