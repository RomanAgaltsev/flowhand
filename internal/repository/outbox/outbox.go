package outbox

import (
	"context"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
)

// Repo is the outbox repository.
//
// WARNING: Phase 0 ships a no-op. Append accepts events and discards them, so
// nothing is published and no outbox row is written. The port exists so the
// Commander's transaction shape is already correct; Phase 1 task T2 replaces
// this with an insert into outbox_events inside the caller's transaction.
type Repo struct{}

// New returns the no-op outbox repository.
func New() *Repo {
	return &Repo{}
}

// Append discards the events and reports success. See the type's warning.
func (r *Repo) Append(_ context.Context, _ ...domain.Event) error {
	return nil
}
