package domain

import (
	"time"

	"github.com/google/uuid"
)

// Event is anything an aggregate emits when its state changes. The same
// value is serialized into outbox_events.payload (see internal/repository/
// outbox) with the envelope version tag below.
type Event interface {
	EventName() string // stable wire identifier, e.g. "task.submitted.v1"
	OccurredAt() time.Time

	// AggregateID answers "which aggregate am I about?". It populates
	// outbox_events.aggregate_id - the Kafka partition key that delivers
	// per-aggregate ordering. NOT NULL there,
	// so an event that cannot answer this cannot be published.
	AggregateID() uuid.UUID
}

// EnvelopeVersion is the schema version of the envelope wrapping every Event
// when it is serialized. Bump when the envelope shape changes (NOT when an
// individual event payload changes - that is encoded in EventName's suffix).
const EnvelopeVersion = 1
