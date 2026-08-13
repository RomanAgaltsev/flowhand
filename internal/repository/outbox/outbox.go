package outbox

import (
	"context"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
)

// Repo is a no-op outbox for now.
type Repo struct{}

// New creates new outbox repo.
func New() *Repo {
	return &Repo{}
}

// Append inserts new events into outbox repo.
func (r *Repo) Append(_ context.Context, _ ...domain.Event) error {
	return nil
}
