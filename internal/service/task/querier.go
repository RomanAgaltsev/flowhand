package task

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TaskView is the read-side projection - denormalized, transport-friendly.
type TaskView struct {
	ID        uuid.UUID
	Status    string
	Handler   string
	CreatedAt time.Time
}

type Querier struct {
	tasks TasksRepo
}

func NewQuerier(tasks TasksRepo) *Querier { return &Querier{tasks: tasks} }

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
