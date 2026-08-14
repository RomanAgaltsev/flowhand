package tasks

import "errors"

// ErrNotFound is returned by repositories when no task matches the lookup.
// Defined here, not in internal/repository, so internal/api can test for it
// without importing the repository layer.
var ErrNotFound = errors.New("task not found")

// ErrConflict is returned when an insert collides with the partial unique
// index on idempotency_key. The service layer turns it into a replay.
var ErrConflict = errors.New("idempotency key conflict")
