//go:build integration

package task_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	repotasks "github.com/RomanAgaltsev/flowhand/internal/repository/tasks"
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
	"github.com/RomanAgaltsev/flowhand/internal/storage/txmgr"
)

// countingRepo counts reads that reach the repository, so the test can tell a
// cache hit from a read-through. Embedding the port keeps every other method
// pointing at the real repository.
type countingRepo struct {
	task.TasksRepo
	gets int
}

func (r *countingRepo) Get(ctx context.Context, id uuid.UUID) (domaintasks.Task, error) {
	r.gets++
	return r.TasksRepo.Get(ctx, id)
}

// TestSchedulerWriteIsVisible is the whole task in one test: a writer that
// holds no cache reference - which is what the scheduler is - moves a task to a
// terminal state, and the next GET sees it. The direct UPDATE stands in for
// CE4, so this runs before CE4 exists.
//
// It also exercises schema.md's read rule end to end: the UPDATE fires the
// archive trigger, so the terminal read is answered from tasks_archive.
func TestSchedulerWriteIsVisible(t *testing.T) {
	pool := startPostgresWithMigrations(t)
	ctx := context.Background()

	repo := &countingRepo{TasksRepo: repotasks.New(txmgr.New(pool))}
	lru, err := task.NewLRUCache(16)
	require.NoError(t, err)
	q := task.NewQuerier(repo, lru, time.Minute)

	handler, err := domaintasks.NewHandler("echo", acceptAnyHandler{})
	require.NoError(t, err)
	submitted, err := domaintasks.Submit(uuid.Must(uuid.NewV7()), handler, 0,
		json.RawMessage(`{}`), 25, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, repo.Insert(ctx, submitted, nil))
	id := submitted.ID()

	// 1. Pending: read through, and NOT cached.
	v, err := q.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "pending", v.Status)

	// 2. The scheduler's write, from outside the service. No cache-clear step -
	// if this test needs one, something non-terminal is being cached.
	_, err = pool.Exec(ctx, `UPDATE tasks SET status = 'succeeded' WHERE id = $1`, id)
	require.NoError(t, err)

	// 3. The next GET sees it - from tasks_archive, through the repository.
	v, err = q.Get(ctx, id)
	require.NoError(t, err, "a finished task must not 404: Get falls back to tasks_archive")
	assert.Equal(t, "succeeded", v.Status)
	assert.Equal(t, 2, repo.gets, "pending was not cached, so this read reached the repository")

	// 4. Terminal now, so cached: the third GET never reaches the repository.
	v, err = q.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", v.Status)
	assert.Equal(t, 2, repo.gets, "a terminal view is served from the cache")
}
