package outbox

import (
	"context"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
)

// Repo is a no-op outbox for now.
type Repo struct{}

func New() *Repo {
	return &Repo{}
}

func (r *Repo) Append(_ context.Context, _ ...domain.Event) error {
	return nil
}
