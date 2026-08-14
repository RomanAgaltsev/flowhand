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

// Querier is the read side of the task service. It shares one repository with
// the Commander today; the split exists so Phase 1 can grow reads their own
// projection and cache without restructuring callers.
type Querier struct {
	tasks TasksRepo
}

// NewQuerier wires the read side over the task repository.
func NewQuerier(tasks TasksRepo) *Querier { return &Querier{tasks: tasks} }

// Get returns the read projection of one task, or the repository's
// domaintasks.ErrNotFound when no such task exists.
func (q *Querier) Get(ctx context.Context, id uuid.UUID) (TaskView, error) {
	row, err := q.tasks.Get(ctx, id)
	if err != nil {
		return TaskView{}, err
	}

	return TaskView{
		ID:        row.ID(),
		Status:    string(row.Status()),
		Handler:   row.Handler(),
		CreatedAt: row.CreatedAt(),
	}, nil
}
