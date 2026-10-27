package task

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TaskView is the read-side projection - denormalized, transport-friendly.
type TaskView struct { //nolint:revive
	ID        uuid.UUID
	Status    string
	Handler   string
	CreatedAt time.Time
}

// TaskCache has no Forget: nothing cached here can change, so there is nothing to
// invalidate. If you find yourself wanting one, something is being cached that
// should not be.
type TaskCache interface { //nolint:revive
	Get(id uuid.UUID) (TaskView, bool)
	Put(id uuid.UUID, v TaskView, ttl time.Duration)
}

// Querier is the read side of the task service. It shares one repository with
// the Commander today; the split exists so Phase 1 can grow reads their own
// projection and cache without restructuring callers.
type Querier struct {
	tasks TasksRepo
	cache TaskCache
	ttl   time.Duration
}

// NewQuerier wires the read side over the task repository.
func NewQuerier(tasks TasksRepo, cache TaskCache, ttl time.Duration) *Querier {
	return &Querier{
		tasks: tasks,
		cache: cache,
		ttl:   ttl,
	}
}

// Get returns the read projection of one task, or the repository's
// domaintasks.ErrNotFound when no such task exists.
func (q *Querier) Get(ctx context.Context, id uuid.UUID) (TaskView, error) {
	if v, ok := q.cache.Get(id); ok {
		return v, nil
	}
	t, err := q.tasks.Get(ctx, id)
	if err != nil {
		return TaskView{}, err
	}
	v := TaskView{
		ID:        t.ID(),
		Status:    string(t.Status()),
		Handler:   t.Handler().Name(),
		CreatedAt: t.CreatedAt(),
	}

	// Cache immutable states ONLY. A running task changes under us by definition, and
	// the scheduler - not this package - is what changes it. This one `if` is the
	// entire invalidation strategy.
	if t.IsTerminal() {
		q.cache.Put(id, v, q.ttl)
	}
	return v, nil
}
