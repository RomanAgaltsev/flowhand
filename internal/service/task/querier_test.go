package task

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
)

func TestQuerierGet_MapsToView(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	created := time.Now().UTC().Truncate(time.Microsecond)
	stored := domaintasks.FromPersistence(id, domaintasks.StatusSucceeded,
		domaintasks.HandlerFromPersistence("echo"), 0,
		json.RawMessage(`{"a":1}`), nil, 0, created, 1, created)

	q := NewQuerier(&fakeTasksRepo{rec: &recorder{}, stored: stored})

	got, err := q.Get(context.Background(), id)
	require.NoError(t, err)

	assert.Equal(t, id, got.ID)
	assert.Equal(t, "succeeded", got.Status) // one vocabulary end to end: domain, database and wire agree
	assert.Equal(t, "echo", got.Handler)
	assert.True(t, created.Equal(got.CreatedAt))
}

func TestQuerierGet_PropagatesNotFound(t *testing.T) {
	q := NewQuerier(&fakeTasksRepo{rec: &recorder{}, storedErr: domaintasks.ErrNotFound})

	_, err := q.Get(context.Background(), uuid.Must(uuid.NewV7()))
	require.ErrorIs(t, err, domaintasks.ErrNotFound)
}
