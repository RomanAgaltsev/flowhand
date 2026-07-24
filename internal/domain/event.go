package domain

import "time"

// Event is anything an aggregate emits when its state changes. The same
// value is serialized into outbox_events.payload (see internal/repository/
// outbox) with the envelope version tag below.
type Event interface {
	EventName() string // stable wire identifier, e.g. "task.submitted.v1"
	OccuredAt() time.Time
}

// EnvelopeVersion is the schema version of the envelope wrapping every Event
// when it is serialized. Bump when the envelope shape changes (NOT when an
// individual event payload changes - that is encoded in EventName's suffix).
const EnvelopeVersion = 1
