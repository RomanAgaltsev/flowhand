package tasks

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
)

// FromPersistence rebuilds an aggregate from stored state. It assigns fields
// directly and deliberately does NOT go through Submit/Start/etc — those emit
// events, and replaying history is not the same thing as it happening again.
// pendingEvents is left nil.
//
// The attempts slice is cloned: the caller keeps no writable handle into the
// aggregate it just built.
func FromPersistence(
	id uuid.UUID,
	status Status,
	handler Handler,
	priority Priority,
	payload json.RawMessage,
	attempts []Attempt,
	leaseEpoch LeaseEpoch,
	earliestAt time.Time,
	maxAttempts uint8,
	createdAt time.Time,
) Task {
	return Task{
		id:          id,
		status:      status,
		handler:     handler,
		priority:    priority,
		payload:     payload,
		attempts:    slices.Clone(attempts),
		leaseEpoch:  leaseEpoch,
		earliestAt:  earliestAt,
		maxAttempts: maxAttempts,
		createdAt:   createdAt,
	}
}

// HandlerFromPersistence rebuilds a Handler from a stored name WITHOUT
// consulting the catalog. Used ONLY by internal/repository/tasks on the read
// path. Exported for the same reason FromPersistence is: the repository is a
// different package, so an unexported constructor would be unreachable there.
//
// The check is skipped on purpose: a row in the database went through
// NewHandler on the way in, so re-validating it would mean threading a
// HandlerCatalog into the repository to re-check data we wrote ourselves.
// Validity is as-of-construction either way (see NewHandler), so the worker
// must still handle "no such handler" at dispatch time.
//
// Not called until D2 widens Task to carry a Handler instead of a bare string.
func HandlerFromPersistence(name string) Handler { return Handler{name: name} }
