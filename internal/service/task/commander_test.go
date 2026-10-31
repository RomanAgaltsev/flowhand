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

	ledger    domaintasks.IdempotencyRecord // what GetIdempotencyRecord returns
	ledgerErr error
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

func (f *fakeTasksRepo) GetIdempotencyRecord(
	_ context.Context, _ domaintasks.Handler, _ domaintasks.IdempotencyKey,
) (domaintasks.IdempotencyRecord, error) {
	f.rec.add("get_record")
	return f.ledger, f.ledgerErr
}

// The T2 port growth in action: the fake must satisfy the whole interface even
// though Submit calls only some of it. They record too, so a future test that
// exercises one fails on an assertion, not on compilation.
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

// fakeIdempStore records through the SHARED recorder, so a test can assert
// that Record happened after tx:commit rather than merely that it happened.
type fakeIdempStore struct {
	rec *recorder

	hitRec    domaintasks.IdempotencyRecord // returned when hit is true
	hit       bool
	lookupErr error // a cache failure; must NOT fail the submit

	recordErr     error
	recorded      domaintasks.IdempotencyRecord
	recordKey     string
	recordHandler string
}

func (f *fakeIdempStore) Lookup(
	_ context.Context, _ domaintasks.Handler, _ domaintasks.IdempotencyKey,
) (domaintasks.IdempotencyRecord, bool, error) {
	f.rec.add("idemp:lookup")
	if f.lookupErr != nil {
		// The real store swallows this, and TestRedisStore_LookupFailsThrough
		// pins that. The fake reports it anyway, so this side proves the
		// Commander falls through even when a store breaks the contract.
		return domaintasks.IdempotencyRecord{}, false, f.lookupErr
	}
	if !f.hit {
		return domaintasks.IdempotencyRecord{}, false, nil
	}
	return f.hitRec, true, nil
}

func (f *fakeIdempStore) Record(
	_ context.Context, handler domaintasks.Handler, key domaintasks.IdempotencyKey, rec domaintasks.IdempotencyRecord,
) error {
	f.rec.add("idemp:record")
	f.recorded, f.recordKey, f.recordHandler = rec, key.String(), handler.Name()
	return f.recordErr
}

func newTestCommander(t *testing.T, repo *fakeTasksRepo, ob *fakeOutbox, tx *fakeTx,
	idemp *fakeIdempStore, id uuid.UUID, now time.Time,
) *Commander {
	t.Helper()
	c := NewCommander(repo, ob, tx, idemp, fakeCatalog{})
	c.now = func() time.Time { return now }
	c.newID = func() (uuid.UUID, error) { return id, nil }
	return c
}

// fingerprint is NewPayloadFingerprint for a payload the test knows is valid.
func fingerprint(t *testing.T, payload string) domaintasks.PayloadFingerprint {
	t.Helper()
	fp, err := domaintasks.NewPayloadFingerprint(json.RawMessage(payload))
	require.NoError(t, err)
	return fp
}

// storedTask is a task already in the store, as a replay would find it.
func storedTask(status domaintasks.Status) domaintasks.Task {
	at := time.Now().UTC()
	return domaintasks.FromPersistence(
		uuid.Must(uuid.NewV7()), status,
		domaintasks.HandlerFromPersistence("echo"), 0,
		json.RawMessage(`{}`), nil, 0, at, 1, at,
	)
}

func TestSubmit_HappyPath(t *testing.T) {
	rec := &recorder{}
	repo := &fakeTasksRepo{rec: rec}
	ob := &fakeOutbox{rec: rec}
	tx := &fakeTx{rec: rec}
	idemp := &fakeIdempStore{rec: rec}

	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := newTestCommander(t, repo, ob, tx, idemp, id, now)

	got, created, err := c.Submit(context.Background(), SubmitCommand{
		Handler: "echo",
		Payload: json.RawMessage(`{"a":1}`),
	})
	require.NoError(t, err)
	assert.True(t, created)

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
		&fakeIdempStore{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, created, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1"})
	require.NoError(t, err)
	assert.True(t, created)

	require.NotNil(t, repo.gotKey)
	assert.Equal(t, "k-1", *repo.gotKey)
}

func TestSubmit_ConflictReplaysExistingTask(t *testing.T) {
	// Cold cache, ledger collision, same payload: the ledger row names the task,
	// Get fetches it (from the archive if it has finished), and the replay
	// re-warms the cache so the next duplicate takes the fast path.
	rec := &recorder{}
	existing := storedTask(domaintasks.StatusRunning)
	repo := &fakeTasksRepo{
		rec:       rec,
		insertErr: fmt.Errorf("insert task: %w", domaintasks.ErrConflict),
		stored:    existing,
		ledger:    domaintasks.IdempotencyRecord{TaskID: existing.ID(), Fingerprint: fingerprint(t, `{}`)},
	}
	ob := &fakeOutbox{rec: rec}
	idemp := &fakeIdempStore{rec: rec}
	c := newTestCommander(t, repo, ob, &fakeTx{rec: rec}, idemp, uuid.Must(uuid.NewV7()), time.Now().UTC())

	got, created, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1"})
	require.NoError(t, err)
	assert.False(t, created, "a replay did not create anything")

	assert.Equal(t, existing.ID(), got.ID())
	// The replay reads AFTER the aborted transaction, and emits no event.
	assert.Equal(t, []string{
		"idemp:lookup", "tx:begin", "insert", "tx:rollback", "get_record", "get", "idemp:record",
	}, rec.calls)
	assert.Empty(t, ob.events)
	assert.Equal(t, existing.ID(), idemp.recorded.TaskID)
}

func TestSubmit_OutboxErrorAborts(t *testing.T) {
	rec := &recorder{}
	wantErr := errors.New("outbox down")
	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec, err: wantErr},
		&fakeTx{rec: rec}, &fakeIdempStore{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, _, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, []string{"tx:begin", "insert", "append", "tx:rollback"}, rec.calls)
}

func TestSubmit_IDErrorTouchesNothing(t *testing.T) {
	rec := &recorder{}
	c := NewCommander(&fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec}, &fakeTx{rec: rec}, &fakeIdempStore{rec: rec}, fakeCatalog{})
	c.newID = func() (uuid.UUID, error) { return uuid.Nil, errors.New("entropy exhausted") }

	_, _, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`)})
	require.ErrorContains(t, err, "entropy exhausted") // not "nil payload": the test must fail for its own reason
	assert.Empty(t, rec.calls, "must fail before opening a transaction")
}

func TestSubmit_ConflictWithoutKeyIsNotReplayed(t *testing.T) {
	rec := &recorder{}
	repo := &fakeTasksRepo{rec: rec, insertErr: fmt.Errorf("insert: %w", domaintasks.ErrConflict)}
	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		&fakeIdempStore{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, _, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`)})
	require.ErrorContains(t, err, "idempotency conflict without a key")
	assert.NotContains(t, rec.calls, "get_record")
}

func TestSubmit_ReplayLookupFails(t *testing.T) {
	rec := &recorder{}
	repo := &fakeTasksRepo{
		rec:       rec,
		insertErr: fmt.Errorf("insert: %w", domaintasks.ErrConflict),
		ledgerErr: errors.New("connection refused"),
	}
	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		&fakeIdempStore{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, _, err := c.Submit(context.Background(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1"})
	require.ErrorContains(t, err, "replay idempotent task")
}

// fakeCatalog stands in for the worker registry. It accepts every name so the
// Commander tests stay about the submit transaction; handler validation itself
// is D1's tests.
type fakeCatalog struct{}

func (fakeCatalog) Has(string) bool { return true }

func TestSubmit_KeylessNeverTouchesRedis(t *testing.T) {
	// A keyless submit has nothing to look up and nothing to warm.
	// NewIdempotencyKey("") is an error, so a Commander that reached for the
	// cache anyway would either log noise or fail outright.
	rec := &recorder{}
	idemp := &fakeIdempStore{rec: rec}
	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec},
		&fakeTx{rec: rec}, idemp, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, _, err := c.Submit(context.Background(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{}`),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"tx:begin", "insert", "append", "tx:commit"}, rec.calls,
		"a keyless submit must not call the cache at all")
}

func TestSubmit_CacheHitShortCircuits(t *testing.T) {
	// The whole point of the fast path: a hit opens NO transaction. The
	// negative is the assertion - a Commander that looked up and then submitted
	// anyway would return the right task and still be wrong.
	rec := &recorder{}
	existing := storedTask(domaintasks.StatusRunning)
	repo := &fakeTasksRepo{rec: rec, stored: existing}
	idemp := &fakeIdempStore{rec: rec, hit: true, hitRec: domaintasks.IdempotencyRecord{
		TaskID: existing.ID(), Fingerprint: fingerprint(t, `{"a":1,"b":2}`),
	}}

	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		idemp, uuid.Must(uuid.NewV7()), time.Now().UTC())

	// The same payload with its keys reordered: the same request.
	got, created, err := c.Submit(context.Background(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"b":2,"a":1}`), IdempotencyKey: "k-1",
	})
	require.NoError(t, err)
	assert.False(t, created)

	assert.Equal(t, existing.ID(), got.ID())
	assert.Equal(t, []string{"idemp:lookup", "get"}, rec.calls,
		"a cache hit must open no transaction and insert nothing")
}

func TestSubmit_FastPathConflict(t *testing.T) {
	// The cache knows the key and the payload differs: 409 territory, decided
	// before an id is minted. Nothing after the lookup may happen - not even
	// the Get, because the answer does not need the task.
	rec := &recorder{}
	existingID := uuid.Must(uuid.NewV7())
	idemp := &fakeIdempStore{rec: rec, hit: true, hitRec: domaintasks.IdempotencyRecord{
		TaskID: existingID, Fingerprint: fingerprint(t, `{"a":1}`),
	}}
	ob := &fakeOutbox{rec: rec}
	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, ob, &fakeTx{rec: rec},
		idemp, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, created, err := c.Submit(context.Background(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"a":2}`), IdempotencyKey: "k-1",
	})
	require.ErrorIs(t, err, ErrIdempotencyConflict)
	ce, ok := errors.AsType[*ConflictError](err)
	require.True(t, ok)
	assert.Equal(t, existingID, ce.ExistingTaskID)
	assert.False(t, created)

	assert.Equal(t, []string{"idemp:lookup"}, rec.calls,
		"a fast-path conflict must open no transaction, insert nothing, append nothing")
	assert.Empty(t, ob.events)
}

func TestSubmit_LedgerConflict(t *testing.T) {
	// Cold cache: the conflict is found where the duplicate is - at the ledger
	// insert. One transaction opens and rolls back; that is the write protocol,
	// not a leak. What must NOT happen is anything after the ledger read: no
	// Get, no event, no cache warm for a submit that was refused.
	rec := &recorder{}
	existingID := uuid.Must(uuid.NewV7())
	repo := &fakeTasksRepo{
		rec:       rec,
		insertErr: fmt.Errorf("insert task: %w", domaintasks.ErrConflict),
		ledger:    domaintasks.IdempotencyRecord{TaskID: existingID, Fingerprint: fingerprint(t, `{"a":1}`)},
	}
	ob := &fakeOutbox{rec: rec}
	c := newTestCommander(t, repo, ob, &fakeTx{rec: rec},
		&fakeIdempStore{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, _, err := c.Submit(context.Background(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"a":2}`), IdempotencyKey: "k-1",
	})
	ce, ok := errors.AsType[*ConflictError](err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, existingID, ce.ExistingTaskID)

	assert.Equal(t, []string{"idemp:lookup", "tx:begin", "insert", "tx:rollback", "get_record"}, rec.calls)
	assert.Empty(t, ob.events)
}

func TestSubmit_InvalidKeyIsRejected(t *testing.T) {
	// D24's backstop. The wire rejects a non-ASCII key at decode; a caller that
	// is not the HTTP API gets the domain's error here, before anything runs.
	rec := &recorder{}
	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		&fakeIdempStore{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, _, err := c.Submit(context.Background(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "ключ",
	})
	require.ErrorIs(t, err, domaintasks.ErrInvalidIdempotencyKey)
	assert.Empty(t, rec.calls, "an invalid key must not reach the cache or the database")
}

func TestSubmit_CacheFailureFallsThrough(t *testing.T) {
	// A degraded cache is not a failed request. This is the test that stops
	// someone "improving" the Commander by propagating Lookup's error; the
	// store's own half is TestRedisStore_LookupFailsThrough.
	rec := &recorder{}
	idemp := &fakeIdempStore{rec: rec, lookupErr: errors.New("dial tcp: connection refused")}
	repo := &fakeTasksRepo{rec: rec}

	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		idemp, uuid.Must(uuid.NewV7()), time.Now().UTC())

	got, created, err := c.Submit(context.Background(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1",
	})
	require.NoError(t, err, "a Redis outage must not fail a submit")
	assert.True(t, created)
	assert.Equal(t, domaintasks.StatusPending, got.Status())
	assert.Equal(t, []string{"idemp:lookup", "tx:begin", "insert", "append", "tx:commit", "idemp:record"},
		rec.calls)
}

func TestSubmit_WarmsCacheAfterCommit(t *testing.T) {
	// "After commit" is the entire property - inside the transaction this
	// advertises an id a rollback would erase. Asserting the ORDER is what
	// makes it a test rather than a coincidence.
	rec := &recorder{}
	id := uuid.Must(uuid.NewV7())
	idemp := &fakeIdempStore{rec: rec}

	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec},
		&fakeTx{rec: rec}, idemp, id, time.Now().UTC())

	_, _, err := c.Submit(context.Background(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"b":2,"a":1}`), IdempotencyKey: "k-1",
	})
	require.NoError(t, err)

	require.Equal(t, []string{"tx:begin", "insert", "append", "tx:commit", "idemp:record"},
		rec.calls[1:], "Record must come after tx:commit, never before or inside")
	assert.Equal(t, domaintasks.IdempotencyRecord{TaskID: id, Fingerprint: fingerprint(t, `{"a":1,"b":2}`)},
		idemp.recorded, "the cache must carry the same canonical fingerprint the ledger does")
	assert.Equal(t, "k-1", idemp.recordKey)
	assert.Equal(t, "echo", idemp.recordHandler)
}

func TestSubmit_WarmFailureIsSoft(t *testing.T) {
	// The task is committed. The client's submit succeeded. A cache write that
	// fails afterwards costs the NEXT duplicate one database round trip and
	// nothing else.
	rec := &recorder{}
	idemp := &fakeIdempStore{rec: rec, recordErr: errors.New("READONLY replica")}

	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec},
		&fakeTx{rec: rec}, idemp, uuid.Must(uuid.NewV7()), time.Now().UTC())

	got, created, err := c.Submit(context.Background(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1",
	})
	require.NoError(t, err, "a failed cache warm must not fail a committed submit")
	assert.True(t, created)
	assert.Equal(t, domaintasks.StatusPending, got.Status())
}
