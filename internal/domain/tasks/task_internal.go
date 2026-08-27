package tasks

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// FromPersistence rebuilds a Task from raw column values. Used ONLY by
// internal/repository/tasks. Not part of the public API.
func FromPersistence(id uuid.UUID, status Status, handler string, payload json.RawMessage, createdAt time.Time) Task {
	return Task{id: id, status: status, handler: handler, payload: payload, createdAt: createdAt}
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
