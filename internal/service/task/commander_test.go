package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	gotClaim  *domaintasks.IdempotencyClaim

	stored    domaintasks.Task
	storedErr error

	ledger    domaintasks.IdempotencyRecord // what GetIdempotencyRecord returns
	ledgerErr error
}

func (f *fakeTasksRepo) Insert(_ context.Context, t domaintasks.Task, claim *domaintasks.IdempotencyClaim) error {
	f.rec.add("insert")
	f.gotTask, f.gotClaim = t, claim
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

// The fake must satisfy the whole interface even
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
	recordCtxErr  error // ctx.Err() as Record saw it
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
	ctx context.Context, handler domaintasks.Handler, key domaintasks.IdempotencyKey, rec domaintasks.IdempotencyRecord,
) error {
	f.rec.add("idemp:record")
	f.recordCtxErr = ctx.Err()
	f.recorded, f.recordKey, f.recordHandler = rec, key.String(), handler.Name()
	return f.recordErr
}

func newTestCommander(t *testing.T, repo *fakeTasksRepo, ob *fakeOutbox, tx *fakeTx,
	idemp *fakeIdempStore, id uuid.UUID, now time.Time,
) *Commander {
	t.Helper()
	c := NewCommander(repo, ob, tx, idemp, fakeCatalog{}, slog.New(slog.DiscardHandler))
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
	now := time.Now().UTC()
	c := newTestCommander(t, repo, ob, tx, idemp, id, now)

	got, created, err := c.Submit(t.Context(), SubmitCommand{
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
	assert.True(t, now.Truncate(time.Microsecond).Equal(got.CreatedAt()))

	assert.Nil(t, repo.gotClaim, "no idempotency key supplied must write no ledger row")
	require.Len(t, ob.events, 1)
	assert.Equal(t, "task.submitted.v1", ob.events[0].EventName())
}

func TestSubmit_PassesIdempotencyKey(t *testing.T) {
	rec := &recorder{}
	repo := &fakeTasksRepo{rec: rec}
	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		&fakeIdempStore{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, created, err := c.Submit(t.Context(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1"})
	require.NoError(t, err)
	assert.True(t, created)

	require.NotNil(t, repo.gotClaim)
	assert.Equal(t, "k-1", repo.gotClaim.Key.String())
	assert.Equal(t, fingerprint(t, `{}`), repo.gotClaim.Fingerprint,
		"the ledger gets the fingerprint the cache will: computed once, here")
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

	got, created, err := c.Submit(t.Context(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1"})
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

	_, _, err := c.Submit(t.Context(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`)})
	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, []string{"tx:begin", "insert", "append", "tx:rollback"}, rec.calls)
}

func TestSubmit_IDErrorTouchesNothing(t *testing.T) {
	rec := &recorder{}
	c := NewCommander(&fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec}, &fakeTx{rec: rec}, &fakeIdempStore{rec: rec}, fakeCatalog{}, slog.New(slog.DiscardHandler))
	c.newID = func() (uuid.UUID, error) { return uuid.Nil, errors.New("entropy exhausted") }

	_, _, err := c.Submit(t.Context(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`)})
	require.ErrorContains(t, err, "entropy exhausted") // not "nil payload": the test must fail for its own reason
	assert.Empty(t, rec.calls, "must fail before opening a transaction")
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

	_, _, err := c.Submit(t.Context(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1"})
	require.ErrorContains(t, err, "replay idempotent task")
}

// fakeCatalog stands in for the worker registry. It accepts every name so the
// Commander tests stay about the submit transaction; handler validation itself
// is tested in the domain.
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

	_, _, err := c.Submit(t.Context(), SubmitCommand{
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
	got, created, err := c.Submit(t.Context(), SubmitCommand{
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
	// before an id is minted. Only the ledger may refuse, so it is asked once.
	// Nothing after that may happen - not even the Get, because the answer does
	// not need the task.
	rec := &recorder{}
	existingID := uuid.Must(uuid.NewV7())
	stored := domaintasks.IdempotencyRecord{TaskID: existingID, Fingerprint: fingerprint(t, `{"a":1}`)}
	idemp := &fakeIdempStore{rec: rec, hit: true, hitRec: stored}
	ob := &fakeOutbox{rec: rec}
	c := newTestCommander(t, &fakeTasksRepo{rec: rec, ledger: stored}, ob, &fakeTx{rec: rec},
		idemp, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, created, err := c.Submit(t.Context(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"a":2}`), IdempotencyKey: "k-1",
	})
	require.ErrorIs(t, err, ErrIdempotencyConflict)
	ce, ok := errors.AsType[*ConflictError](err)
	require.True(t, ok)
	assert.Equal(t, existingID, ce.ExistingTaskID)
	assert.False(t, created)

	assert.Equal(t, []string{"idemp:lookup", "get_record"}, rec.calls,
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

	_, _, err := c.Submit(t.Context(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"a":2}`), IdempotencyKey: "k-1",
	})
	ce, ok := errors.AsType[*ConflictError](err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, existingID, ce.ExistingTaskID)

	assert.Equal(t, []string{"idemp:lookup", "tx:begin", "insert", "tx:rollback", "get_record"}, rec.calls)
	assert.Empty(t, ob.events)
}

func TestSubmit_InvalidKeyIsRejected(t *testing.T) {
	// The backstop. The wire rejects a non-ASCII key at decode; a caller that
	// is not the HTTP API gets the domain's error here, before anything runs.
	rec := &recorder{}
	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		&fakeIdempStore{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, _, err := c.Submit(t.Context(), SubmitCommand{
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

	got, created, err := c.Submit(t.Context(), SubmitCommand{
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

	_, _, err := c.Submit(t.Context(), SubmitCommand{
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

	got, created, err := c.Submit(t.Context(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1",
	})
	require.NoError(t, err, "a failed cache warm must not fail a committed submit")
	assert.True(t, created)
	assert.Equal(t, domaintasks.StatusPending, got.Status())
}

func TestSubmit_StaleCacheMismatchIsNotAConflict(t *testing.T) {
	// The cache holds a fingerprint the ledger does not: a restore, or a database
	// recreated under a Redis that kept its volume. The ledger agrees with this
	// submit, so it is a replay. A 409 here is a refusal the ledger never issued.
	rec := &recorder{}
	existing := storedTask(domaintasks.StatusRunning)
	ledger := domaintasks.IdempotencyRecord{TaskID: existing.ID(), Fingerprint: fingerprint(t, `{"a":2}`)}
	repo := &fakeTasksRepo{rec: rec, stored: existing, ledger: ledger}
	idemp := &fakeIdempStore{rec: rec, hit: true, hitRec: domaintasks.IdempotencyRecord{
		TaskID: uuid.Must(uuid.NewV7()), Fingerprint: fingerprint(t, `{"a":1}`),
	}}
	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		idemp, uuid.Must(uuid.NewV7()), time.Now().UTC())

	got, created, err := c.Submit(t.Context(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"a":2}`), IdempotencyKey: "k-1",
	})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, existing.ID(), got.ID())
	assert.Equal(t, []string{"idemp:lookup", "get_record", "idemp:record", "get"}, rec.calls)
	assert.Equal(t, ledger, idemp.recorded, "the stale entry must be replaced with what the ledger says")
}

func TestSubmit_StaleCacheTheLedgerNeverSawCreates(t *testing.T) {
	// The cache outlived the ledger entirely. The key is free, so the submit
	// creates, and the warm after commit overwrites the stale entry.
	rec := &recorder{}
	id := uuid.Must(uuid.NewV7())
	repo := &fakeTasksRepo{rec: rec, ledgerErr: fmt.Errorf("read: %w", domaintasks.ErrNotFound)}
	idemp := &fakeIdempStore{rec: rec, hit: true, hitRec: domaintasks.IdempotencyRecord{
		TaskID: uuid.Must(uuid.NewV7()), Fingerprint: fingerprint(t, `{"a":1}`),
	}}
	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec}, idemp, id, time.Now().UTC())

	got, created, err := c.Submit(t.Context(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"a":2}`), IdempotencyKey: "k-1",
	})
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, id, got.ID())
	assert.Equal(t, []string{
		"idemp:lookup", "get_record", "tx:begin", "insert", "append", "tx:commit", "idemp:record",
	}, rec.calls)
	assert.Equal(t, id, idemp.recorded.TaskID)
}

func TestSubmit_LedgerReadFailureLeavesTheTransactionToDecide(t *testing.T) {
	// A failed confirmation is not a refusal. The insert is what finds out whether
	// the key is taken, and replay is what reports a ledger that cannot be read.
	rec := &recorder{}
	repo := &fakeTasksRepo{
		rec:       rec,
		ledgerErr: errors.New("connection refused"),
		insertErr: fmt.Errorf("insert task: %w", domaintasks.ErrConflict),
	}
	idemp := &fakeIdempStore{rec: rec, hit: true, hitRec: domaintasks.IdempotencyRecord{
		TaskID: uuid.Must(uuid.NewV7()), Fingerprint: fingerprint(t, `{"a":1}`),
	}}
	c := newTestCommander(t, repo, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
		idemp, uuid.Must(uuid.NewV7()), time.Now().UTC())

	_, _, err := c.Submit(t.Context(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{"a":2}`), IdempotencyKey: "k-1",
	})
	require.ErrorContains(t, err, "replay idempotent task")
	assert.NotErrorIs(t, err, ErrIdempotencyConflict)
}

func TestSubmit_InvalidPayloadIsRejected(t *testing.T) {
	// Keyed and keyless alike. A keyless submit is never fingerprinted, so this is
	// the case that needs the aggregate to do the checking. The key is what the
	// keyed row adds: its payload must be refused before the cache is consulted.
	for name, key := range map[string]string{"keyed": "k-1", "keyless": ""} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			c := newTestCommander(t, &fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec}, &fakeTx{rec: rec},
				&fakeIdempStore{rec: rec}, uuid.Must(uuid.NewV7()), time.Now().UTC())

			_, _, err := c.Submit(t.Context(), SubmitCommand{
				Handler: "echo", Payload: json.RawMessage(`{"a":"\u0000"}`), IdempotencyKey: key,
			})
			require.ErrorIs(t, err, domaintasks.ErrInvalidPayload)
			assert.Empty(t, rec.calls, "a payload the store refuses must reach neither the cache nor a transaction")
		})
	}
}

func TestSubmit_ClockIsTruncatedToTheColumn(t *testing.T) {
	// timestamptz keeps microseconds. Untruncated, the aggregate and its event would
	// carry an instant the row cannot, and the two would disagree on when the task
	// was created. The clock is deliberately not on a microsecond boundary.
	rec := &recorder{}
	ob := &fakeOutbox{rec: rec}
	clock := time.Date(2026, 11, 5, 12, 0, 0, 123456789, time.UTC)
	want := time.Date(2026, 11, 5, 12, 0, 0, 123456000, time.UTC)
	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, ob, &fakeTx{rec: rec},
		&fakeIdempStore{rec: rec}, uuid.Must(uuid.NewV7()), clock)

	got, _, err := c.Submit(t.Context(), SubmitCommand{Handler: "echo", Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)

	assert.True(t, want.Equal(got.CreatedAt()), "got %s", got.CreatedAt())
	assert.True(t, want.Equal(got.EarliestAt()), "got %s", got.EarliestAt())
	require.Len(t, ob.events, 1)
	assert.True(t, want.Equal(ob.events[0].OccurredAt()), "got %s", ob.events[0].OccurredAt())
}

func TestSubmit_WarmSurvivesTheClientLeaving(t *testing.T) {
	// The task is committed, so a client that disconnects now must not cost it
	// its cache entry. The transaction runs on the request's context; canceling
	// it inside the fake models a disconnect in the window after the commit.
	rec := &recorder{}
	idemp := &fakeIdempStore{rec: rec}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec},
		&fakeTx{rec: rec}, idemp, uuid.Must(uuid.NewV7()), time.Now().UTC())
	c.tx = cancelAfterCommit{inner: c.tx, cancel: cancel}

	_, created, err := c.Submit(ctx, SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1",
	})
	require.NoError(t, err)
	assert.True(t, created)
	require.Error(t, ctx.Err(), "the precondition: the request context really is gone")
	assert.NoError(t, idemp.recordCtxErr, "the warm must not inherit the request's cancellation")
}

// cancelAfterCommit cancels the request's context the moment the transaction
// has committed.
type cancelAfterCommit struct {
	inner  TxRunner
	cancel context.CancelFunc
}

func (c cancelAfterCommit) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	err := c.inner.WithinTx(ctx, fn)
	c.cancel()
	return err
}

func TestSubmit_WarmFailureLogsFlatAttributes(t *testing.T) {
	// Under the JSON handler a value with unexported fields logs as {}, which once
	// lost the one field an operator needs from this line.
	rec := &recorder{}
	var logs bytes.Buffer
	id := uuid.Must(uuid.NewV7())
	idemp := &fakeIdempStore{rec: rec, recordErr: errors.New("READONLY replica")}
	c := newTestCommander(t, &fakeTasksRepo{rec: rec}, &fakeOutbox{rec: rec},
		&fakeTx{rec: rec}, idemp, id, time.Now().UTC())
	c.log = slog.New(slog.NewJSONHandler(&logs, nil))

	_, _, err := c.Submit(t.Context(), SubmitCommand{
		Handler: "echo", Payload: json.RawMessage(`{}`), IdempotencyKey: "k-1",
	})
	require.NoError(t, err)

	assert.Contains(t, logs.String(), `"handler":"echo"`)
	assert.Contains(t, logs.String(), `"idempotency_key":"k-1"`)
	assert.Contains(t, logs.String(), `"task_id":"`+id.String()+`"`)
}
