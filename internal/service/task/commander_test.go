package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/domain"
	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
)

// recorder is shared by all three fakes so a test can assert the ORDER of
// calls across them, not merely that each happened.
type recorder struct{ calls []string }

func (r *recorder) add(s string) { r.calls = append(r.calls, s) }

type fakeTasksRepo struct {
	rec *recorder

	insertErr error
	gotTask   domaintasks.Task
	gotKey    *string

	stored    domaintasks.Task
	storedErr error
}

func (f *fakeTasksRepo) Insert(_ context.Context, t domaintasks.Task, key *string) error {
	f.rec.add("insert")
	f.gotTask, f.gotKey = t, key
	return f.insertErr
}

func (f *fakeTasksRepo) Get(_ context.Context, _ uuid.UUID) (domaintasks.Task, error) {
	f.rec.add("get")
	return f.stored, f.storedErr
}

func (f *fakeTasksRepo) GetByIdempotencyKey(_ context.Context, _ string) (domaintasks.Task, error) {
	f.rec.add("get_by_key")
	return f.stored, f.storedErr
}

// The T2 port growth in action: the fake must satisfy the whole interface even
// though Submit only calls Insert/Get/GetByIdempotencyKey. They record too, so
// a future test that exercises one fails on an assertion, not on compilation.
func (f *fakeTasksRepo) GetForUpdate(_ context.Context, _ uuid.UUID) (domaintasks.Task, error) {
	f.rec.add("get_for_update")
	return f.stored, f.storedErr
}

func (f *fakeTasksRepo) PersistTransition(_ context.Context, _ domaintasks.Task, _ int64) (int64, error) {
	f.rec.add("persist_transition")
	return 1, nil // 1 row: the fence passed
}

func (f *fakeTasksRepo) ExtendLease(_ context.Context, _ uuid.UUID, _ time.Time, _ int64, _ time.Duration) (int64, error) {
	f.rec.add("extend_lease")
	return 1, nil
}

type fakeOutbox struct {
	rec    *recorder
	err    error
	events []domain.Event
}

func (f *fakeOutbox) Append(_ context.Context, events ...domain.Event) error {
	f.rec.add("append")
	f.events = append(f.events, events...)
	return f.err
}

type fakeTx struct{ rec *recorder }

func (f *fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	f.rec.add("tx:begin")
	if err := fn(ctx); err != nil {
		f.rec.add("tx:rollback")
		return err
	}
	f.rec.add("tx:commit")
	return nil
}

func newTestCommander(t *testing.T, repo *fakeTasksRepo, ob *fakeOutbox, tx *fakeTx,
	id uuid.UUID, now time.Time,
) *Commander {
	t.Helper()
	c := NewCommander(repo, ob, tx, fakeCatalog{})
	c.now = func() time.Time { return now }
	c.newID = func() (uuid.UUID, error) { return id, nil }
	return c
}

func TestSubmit_HappyPath(t *testing.T) {
	rec := &recorder{}
	repo := &fakeTasksRepo{rec: rec}
	ob := &fakeOutbox{rec: rec}
	tx := &fakeTx{rec: rec}

	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := newTestCommander(t, repo, ob, tx, id, now)

	got, err := c.Submit(context.Background(), SubmitCommand{
		Handler: "echo",
		Payload: json.RawMessage(`{"a":1}`),
	})
	require.NoError(t, err)

	// The ordering assertion is the point of the test: both writes happen
	// inside the transaction, insert before append.
	assert.Equal(t, []string{"tx:begin", "insert", "append", "tx:commit"}, rec.calls)

	assert.Equal(t, id, got.ID())
	assert.Equal(t, domaintasks.StatusPending, got.Status())
	assert.Equal(t, "echo", got.Handler().Name())
	assert.True(t, now.Equal(got.CreatedAt()))

	assert.Nil(t, repo.gotKey, "no idempotency key supplied must reach the DB as NULL, not \"\"")
	require.Len(t, ob.events, 1)
	assert.Equal(t, "task.submitted.v1", ob.events[0].EventName())
}

func TestSubmit_PassesIdempotencyKey(t *testing.T) {
	rec := &recorder{}
	repo := &fakeTasksRepo{rec: rec}
	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1"})
	require.NoError(t, err)

	require.NotNil(t, repo.gotKey)
	assert.Equal(t, "k-1", *repo.gotKey)
}

func TestSubmit_ConflictReplaysExistingTask(t *testing.T) {
	rec := &recorder{}
	storedAt := time.Now().UTC()
	existing := domaintasks.FromPersistence(
		uuid.Must(uuid.NewV7()), domaintasks.StatusRunning,
		domaintasks.HandlerFromPersistence("echo"), 0,
		json.RawMessage(`{}`), nil, 0, storedAt, 1, storedAt,
	)
	repo := &fakeTasksRepo{
		rec:       rec,
		insertErr: fmt.Errorf("insert task: %w", domaintasks.ErrConflict),
		stored:    existing,
	}
	ob := &fakeOutbox{rec: rec}
	c := newTestCommander(t, repo, ob, &fakeTx{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

	got, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1"})
	require.NoError(t, err)

	assert.Equal(t, existing.ID(), got.ID())
	// Replay reads AFTER the aborted transaction, and emits no event.
	assert.Equal(t, []string{"tx:begin", "insert", "tx:rollback", "get_by_key"}, rec.calls)
	assert.Empty(t, ob.events)
}

func TestSubmit_OutboxErrorAborts(t *testing.T) {
	rec := &recorder{}
	wantErr := errors.New("outbox down")
	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec, err: wantErr},
		&fakeTx{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, []string{"tx:begin", "insert", "append", "tx:rollback"}, rec.calls)
}

func TestSubmit_IDErrorTouchesNothing(t *testing.T) {
	rec := &recorder{}
	c := NewCommander(&fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec}, &fakeTx{rec: rec}, fakeCatalog{})
	c.newID = func() (uuid.UUID, error) { return uuid.Nil, errors.New("entropy exhausted") }

	_, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`)})
	require.ErrorContains(t, err, "entropy exhausted") // not "nil payload": the test must fail for its own reason
	assert.Empty(t, rec.calls, "must fail before opening a transaction")
}

func TestSubmit_ConflictWithoutKeyIsNotReplayed(t *testing.T) {
	rec := &recorder{}
	repo := &fakeTasksRepo{rec: rec, insertErr: fmt.Errorf("insert: %w", domaintasks.ErrConflict)}
	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`)})
	require.ErrorContains(t, err, "idempotency conflict without a key")
	assert.NotContains(t, rec.calls, "get_by_key")
}

func TestSubmit_ReplayLookupFails(t *testing.T) {
	rec := &recorder{}
	repo := &fakeTasksRepo{
		rec:       rec,
		insertErr: fmt.Errorf("insert: %w", domaintasks.ErrConflict),
		storedErr: errors.New("connection refused"),
	}
	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1"})
	require.ErrorContains(t, err, "replay idempotent task")
}

// fakeCatalog stands in for the worker registry. It accepts every name so the
// Commander tests stay about the submit transaction; handler validation itself
// is D1's tests.
type fakeCatalog struct{}

func (fakeCatalog) Has(string) bool { return true }
