-- Grows `tasks` from the minimal submit-path table into the full queue row, and adds
-- the tables the runtime needs around it: an append-only attempt log, a transactional
-- outbox, a dead-letter table, cron registrations, an idempotency ledger and a
-- partitioned archive.

-- +goose Up

-- Adding a NOT NULL column to a table that already has rows fails unless a DEFAULT
-- backfills them. Leaving that DEFAULT in place afterwards means an INSERT that
-- forgets the column silently gets the default forever, which is a class of bug that
-- never surfaces; dropping it makes Postgres reject the incomplete INSERT loudly.
-- Hence the ADD-then-DROP DEFAULT pair on every NOT NULL column below.
--
-- uuidv7() is a Postgres 18 builtin (no extension required). Time-ordered UUIDs keep
-- B-tree inserts local to the rightmost page instead of scattering them like v4 --
-- see the new tables below, which use it for their primary keys.

-- The single-tenant sentinel, matching uuid.Nil on the Go side. A CONSTANT default
-- keeps this ADD COLUMN metadata-only; uuidv7() here would be volatile, rewriting the
-- whole table under ACCESS EXCLUSIVE and handing every pre-existing row a distinct
-- tenant that does not exist.
ALTER TABLE tasks ADD COLUMN tenant_id UUID NOT NULL
    DEFAULT '00000000-0000-0000-0000-000000000000';
ALTER TABLE tasks ALTER COLUMN tenant_id DROP DEFAULT;

-- Higher runs sooner.
ALTER TABLE tasks ADD COLUMN priority SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE tasks ALTER COLUMN priority DROP DEFAULT;

-- Dequeue gate: holds both the submit-time delay and the retry backoff. A task is
-- invisible to the dequeue scan until earliest_at <= now().
ALTER TABLE tasks ADD COLUMN earliest_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE tasks ALTER COLUMN earliest_at DROP DEFAULT;

ALTER TABLE tasks ADD COLUMN attempt INT NOT NULL DEFAULT 0;
ALTER TABLE tasks ALTER COLUMN attempt DROP DEFAULT;

ALTER TABLE tasks ADD COLUMN max_attempts INT NOT NULL DEFAULT 25;
ALTER TABLE tasks ALTER COLUMN max_attempts DROP DEFAULT;

-- Consistent-hash shard. Schedulers own a subset and scan only their own shards.
ALTER TABLE tasks ADD COLUMN shard_id INT NOT NULL DEFAULT 0;
ALTER TABLE tasks ALTER COLUMN shard_id DROP DEFAULT;

-- A flag rather than a status, because the two are independent: a task can be
-- `running` and cancel-requested at the same time, which one status column cannot
-- express. The worker observes the flag and stops at its next checkpoint.
ALTER TABLE tasks ADD COLUMN cancel_requested BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tasks ALTER COLUMN cancel_requested DROP DEFAULT;

ALTER TABLE tasks ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE tasks ALTER COLUMN updated_at DROP DEFAULT;

-- Nullable, so no DEFAULT to add and none to drop.
--
-- lease_until / lease_epoch are the lease: a worker holds the row until lease_until,
-- and lease_epoch is the fencing token stamped at dispatch. Every write that mutates
-- a leased row carries the epoch it was dispatched with in its WHERE clause, so a
-- worker whose lease was reclaimed underneath it updates zero rows instead of
-- clobbering the new owner's work.
ALTER TABLE tasks ADD COLUMN started_at TIMESTAMPTZ;
ALTER TABLE tasks ADD COLUMN worker_id TEXT;
ALTER TABLE tasks ADD COLUMN lease_until TIMESTAMPTZ;
ALTER TABLE tasks ADD COLUMN lease_epoch BIGINT;
ALTER TABLE tasks ADD COLUMN trace_id TEXT;

-- Deduplication moves to the idempotency_keys ledger below. A uniqueness floor on
-- `tasks` cannot hold: the archive trigger deletes the row the moment the task
-- finishes, taking the index entry with it, so a replayed request would be accepted
-- as new. The old index was also keyed on the bare idempotency_key, which collides
-- across tenants.
DROP INDEX tasks_idempotency_key_uniq;

-- The database's status vocabulary. A CHECK rather than an enum type: adding a value
-- later is one ALTER TABLE, whereas ALTER TYPE ... ADD VALUE cannot be used in the
-- same transaction that adds it.
--
-- These are the same words the domain and the HTTP API use -- one vocabulary, spelled
-- once. `canceled` carries a single L to match the standard library's
-- context.Canceled, which the cancellation path sits directly alongside.
-- internal/api/mapper.go still stands between this column and the wire, but as a
-- rename rather than a translation; it is the seam to widen if the two ever need to
-- diverge for a reason of meaning rather than of spelling.
--
-- There is no state for "lease expired". The reclaim sweep moves such a row
-- `running` -> `pending` in a single UPDATE, so no row ever holds that value and it
-- needs no spelling here.
--
-- `failed` is written only when the failure is final. A transient failure goes
-- `running` -> `retry_scheduled` directly and never passes through `failed`, because
-- the archive trigger treats `failed` as terminal and would move the row out
-- mid-retry. Per-attempt failures are recorded on task_attempts instead.
ALTER TABLE tasks ADD CONSTRAINT tasks_status_check CHECK (status IN (
    'pending', 'running', 'retry_scheduled',
    'succeeded', 'failed', 'canceled', 'dead_lettered'
));

-- One immutable row per execution attempt, and the sink workers heartbeat into.
-- Heartbeats land here rather than on `tasks` so the hot table's write rate stays
-- proportional to state transitions instead of to heartbeat frequency -- which keeps
-- its HOT-update ratio high and its dead-tuple churn low.
--
-- `status` is intentionally not covered by tasks_status_check. A per-attempt `failed`
-- is ordinary and frequent, whereas on `tasks` it is terminal.
CREATE TABLE task_attempts (
    id             UUID        PRIMARY KEY DEFAULT uuidv7(),
    task_id        UUID        NOT NULL,
    attempt        INT         NOT NULL,
    worker_id      TEXT        NOT NULL,
    status         TEXT        NOT NULL,
    started_at     TIMESTAMPTZ NOT NULL,
    finished_at    TIMESTAMPTZ,
    last_heartbeat TIMESTAMPTZ,
    progress_pct   REAL,
    error_class    TEXT,
    error_message  TEXT,
    error_stack    TEXT
);

CREATE INDEX idx_task_attempts_task ON task_attempts (task_id, attempt);

-- Transactional outbox. A state change and its event are written in one transaction,
-- so the event becomes durable at the same instant as the change it describes; the
-- relay then tails this table and publishes at-least-once. This sidesteps the fact
-- that you cannot atomically commit to Postgres and publish to a broker.
--
-- aggregate_id is NOT NULL because it is the partition key on the wire -- it is what
-- delivers per-aggregate ordering to consumers.
--
-- Two different version numbers, on purpose: the `.vN` suffix on `type` versions the
-- payload schema and consumers may see several in flight, while envelope_ver versions
-- the wrapping and bumping it requires every consumer to migrate at once. This column
-- is the only home for envelope_ver; it is not repeated inside `payload`.
CREATE TABLE outbox_events (
    event_id     UUID        PRIMARY KEY DEFAULT uuidv7(),
    aggregate_id UUID        NOT NULL,
    type         TEXT        NOT NULL,
    envelope_ver SMALLINT    NOT NULL DEFAULT 1,
    trace_id     TEXT,
    payload      JSONB       NOT NULL,
    sent         BOOLEAN     NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Where poison messages come to rest, and what `ctl dlq replay` reads back.
-- `reason` records how the task got here -- 'poison:<class>', 'max_attempts_exhausted'
-- or 'wall_clock_exceeded' -- which is what makes triage possible without replaying.
CREATE TABLE dlq_tasks (
    id               UUID        PRIMARY KEY DEFAULT uuidv7(),
    task_id          UUID        NOT NULL,
    handler          TEXT        NOT NULL,
    payload          JSONB       NOT NULL,
    error_class      TEXT        NOT NULL,
    reason           TEXT        NOT NULL,
    attempts         INT         NOT NULL,
    dead_lettered_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_dlq_handler ON dlq_tasks (handler, dead_lettered_at);

-- Cron registrations. next_fire_at is computed on every fire and is what the due-scan
-- rides; timezone is an IANA name, so DST transitions are resolved against it rather
-- than against UTC offsets.
CREATE TABLE schedules (
    id             UUID        PRIMARY KEY DEFAULT uuidv7(),
    tenant_id      UUID        NOT NULL,
    handler        TEXT        NOT NULL,
    cron           TEXT        NOT NULL,
    timezone       TEXT        NOT NULL DEFAULT 'UTC',
    payload        JSONB       NOT NULL,
    catchup_policy TEXT        NOT NULL DEFAULT 'skip',
    overlap_policy TEXT        NOT NULL DEFAULT 'skip',
    last_fired_at  TIMESTAMPTZ,
    next_fire_at   TIMESTAMPTZ NOT NULL,
    shard_id       INT         NOT NULL,
    enabled        BOOLEAN     NOT NULL DEFAULT true,

    -- catchup_policy decides what happens to fires missed while the scheduler was
    -- down; overlap_policy decides what happens when a fire comes due while the
    -- previous run is still going. Both are closed vocabularies, so a typo should
    -- fail at the write rather than at 03:00 when the schedule fires.
    CONSTRAINT schedules_catchup_policy_check
        CHECK (catchup_policy IN ('skip', 'fire_once', 'fire_all')),
    CONSTRAINT schedules_overlap_policy_check
        CHECK (overlap_policy IN ('allow', 'skip', 'queue'))
);

-- The durable deduplication ledger. A row is inserted in the same transaction as the
-- task it belongs to, and the primary-key violation *is* the duplicate signal -- no
-- read-then-write race to lose. Redis in front of this is a read-through accelerator,
-- never the source of truth.
--
-- The key is the composite (tenant_id, handler, idempotency_key): scoping it to the
-- bare key would let two tenants collide on "retry-1". payload_hash backs the
-- conflict check, so reusing a key with a different body is rejected rather than
-- silently returning the first task.
--
-- This lives outside `tasks` so the guarantee survives archival -- see the DROP INDEX
-- above.
CREATE TABLE idempotency_keys (
    tenant_id       UUID        NOT NULL,
    handler         TEXT        NOT NULL,
    idempotency_key TEXT        NOT NULL,
    task_id         UUID        NOT NULL,
    payload_hash    BYTEA       NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT now() + interval '90 days',
    PRIMARY KEY (tenant_id, handler, idempotency_key)
);

-- Retention sweep: DELETE ... WHERE expires_at < now(), batched.
CREATE INDEX idx_idempotency_expiry ON idempotency_keys (expires_at);

-- The cold table. Finished tasks are moved here by trg_tasks_archive below, which is
-- why reads have to fall back: a task that completed a second ago is no longer in
-- `tasks`, so a lookup misses the hot table and must try here before answering 404.
--
-- The columns mirror `tasks` in its *physical* order (the original five, then
-- handler, then everything this migration appends -- ALTER TABLE ADD COLUMN always
-- appends). Keep the two in lockstep: any column added to `tasks` must be added here
-- and to the trigger's INSERT list, or results silently lose a field. A cheap guard:
--
--   SELECT column_name FROM information_schema.columns WHERE table_name = 'tasks'
--   EXCEPT
--   SELECT column_name FROM information_schema.columns WHERE table_name='tasks_archive';
--
-- must return zero rows.
--
-- No per-column defaults: every row arrives fully populated from the trigger, so a
-- default here could only ever mask a trigger bug.
--
-- The result columns live only here, never on `tasks`. Writing a result is therefore
-- two statements in one transaction: the terminal UPDATE on `tasks` (which fires the
-- trigger and moves the row), then an UPDATE on tasks_archive carrying the payload.
--
-- The primary key is the composite (id, finished_at) because Postgres requires a
-- unique constraint on a partitioned table to include every partitioning column.
-- Consequence: a lookup by id alone cannot prune to a single partition. That is fine
-- while only one or two partitions are live, but a query that can supply finished_at
-- should.
CREATE TABLE tasks_archive (
    id                UUID         NOT NULL,
    idempotency_key   TEXT,
    payload           JSONB        NOT NULL,
    status            TEXT         NOT NULL,
    created_at        TIMESTAMPTZ  NOT NULL,
    handler           TEXT         NOT NULL,
    tenant_id         UUID         NOT NULL,
    priority          SMALLINT     NOT NULL,
    earliest_at       TIMESTAMPTZ  NOT NULL,
    attempt           INT          NOT NULL,
    max_attempts      INT          NOT NULL,
    shard_id          INT          NOT NULL,
    cancel_requested  BOOLEAN      NOT NULL,
    updated_at        TIMESTAMPTZ  NOT NULL,
    started_at        TIMESTAMPTZ,
    worker_id         TEXT,
    lease_until       TIMESTAMPTZ,
    lease_epoch       BIGINT,
    trace_id          TEXT,
    finished_at       TIMESTAMPTZ  NOT NULL,
    result            JSONB,
    result_url        TEXT,
    result_size_bytes BIGINT,
    PRIMARY KEY (id, finished_at)
) PARTITION BY RANGE (finished_at);

-- Monthly partitions, provisioned ahead of the clock by a partition-manager job. This
-- migration seeds the first two by hand so there is somewhere to write on day one.
CREATE TABLE tasks_archive_2026_08 PARTITION OF tasks_archive
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE tasks_archive_2026_09 PARTITION OF tasks_archive
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');

-- Safety net. Without a default partition, a finished task whose finished_at falls
-- outside every declared range aborts the worker's whole result transaction. Keep it
-- empty: attaching a new range partition has to scan the default to prove no row
-- belongs in the new range, and that scan takes an ACCESS EXCLUSIVE lock.
CREATE TABLE tasks_archive_default PARTITION OF tasks_archive DEFAULT;

-- The dequeue scan:
--
--   SELECT id FROM tasks
--    WHERE status = 'pending' AND earliest_at <= now() AND shard_id = ANY($1)
--    ORDER BY priority DESC, earliest_at ASC, id
--    LIMIT $2 FOR UPDATE SKIP LOCKED
--
-- Partial, because the overwhelming majority of rows are not pending -- that is the
-- difference between an index that stays in memory and one that does not. The
-- planner will only use it when it can prove the query's WHERE implies the index
-- predicate, so the `pending` literal has to match exactly. Column order mirrors the
-- ORDER BY so the scan comes out pre-sorted.
CREATE INDEX idx_tasks_dequeue
    ON tasks (shard_id, priority DESC, earliest_at ASC, id)
    WHERE status = 'pending';

-- Finds leases that lapsed: WHERE status = 'running' AND lease_until < now().
CREATE INDEX idx_tasks_lease_reclaim
    ON tasks (lease_until)
    WHERE status = 'running';

-- The relay's scan for unpublished events, in insertion order.
CREATE INDEX idx_outbox_unsent
    ON outbox_events (created_at)
    WHERE sent = false;

-- The scheduler's scan for schedules that have come due.
CREATE INDEX idx_schedules_due
    ON schedules (next_fire_at)
    WHERE enabled;

-- Wakes idle dequeue loops instead of making them poll. Notifications are delivered
-- at COMMIT, never before, so a listener never sees a task it cannot yet read.
--
-- INSERT only. A retry is an UPDATE (status back to pending, earliest_at pushed into
-- the future) and deliberately does not notify: there is nothing to wake up *for*
-- until earliest_at arrives, and the scheduler's next tick picks it up.
-- +goose StatementBegin
CREATE FUNCTION notify_tasks_available() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- The payload is the shard, so a scheduler owning shards {0,2} can drop a wake
    -- for shard 7 without a round-trip to the database.
    --
    -- One notification per row on a batch insert, but Postgres collapses identical
    -- (channel, payload) pairs within a transaction, so a bulk submit into one shard
    -- still yields one wake.
    PERFORM pg_notify('tasks_available', NEW.shard_id::TEXT);
    RETURN NULL; -- AFTER trigger: the return value is ignored
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_tasks_notify
    AFTER INSERT ON tasks
    FOR EACH ROW
    WHEN (NEW.status = 'pending')
    EXECUTE FUNCTION notify_tasks_available();

-- Moves a task out of the hot table the moment it reaches a terminal state, so
-- `tasks` stays roughly the size of the working set rather than growing forever.
--
-- AFTER, not BEFORE: a BEFORE trigger runs while the row is mid-update and cannot
-- delete it. The DELETE fires no UPDATE triggers, so there is no recursion.
-- +goose StatementBegin
CREATE FUNCTION move_to_archive() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- An explicit column list, not `SELECT NEW.*`: the two tables hold the same
    -- columns but Postgres matches positionally, and `tasks`' physical order is
    -- whatever the migrations happened to append. Add a column to `tasks` and it must
    -- be added to tasks_archive and to both lists below.
    INSERT INTO tasks_archive (
        id, idempotency_key, payload, status, created_at, handler, tenant_id,
        priority, earliest_at, attempt, max_attempts, shard_id, cancel_requested,
        updated_at, started_at, worker_id, lease_until, lease_epoch, trace_id,
        finished_at
    ) VALUES (
        NEW.id, NEW.idempotency_key, NEW.payload, NEW.status, NEW.created_at,
        NEW.handler, NEW.tenant_id, NEW.priority, NEW.earliest_at, NEW.attempt,
        NEW.max_attempts, NEW.shard_id, NEW.cancel_requested, NEW.updated_at,
        NEW.started_at, NEW.worker_id, NEW.lease_until, NEW.lease_epoch,
        NEW.trace_id, now()
    );

    -- result / result_url / result_size_bytes stay NULL here. The caller fills them
    -- with a second statement against tasks_archive, in the same transaction as the
    -- terminal UPDATE that got us here -- this trigger completes within that first
    -- statement, so the archive row is already visible to the second.

    DELETE FROM tasks WHERE id = NEW.id;
    RETURN NULL; -- the row is gone from the hot table
END;
$$;
-- +goose StatementEnd

-- The spellings below must be the ones in tasks_status_check; a status this trigger
-- watches for but the constraint forbids can never occur, and the trigger would
-- silently never fire for it.
--
-- `AFTER UPDATE OF status` fires whenever status appears in the SET list even if the
-- value is unchanged, hence the IS DISTINCT FROM guard.
CREATE TRIGGER trg_tasks_archive
    AFTER UPDATE OF status ON tasks
    FOR EACH ROW
    WHEN (NEW.status IS DISTINCT FROM OLD.status
        AND NEW.status IN ('succeeded', 'failed', 'canceled', 'dead_lettered'))
    EXECUTE FUNCTION move_to_archive();

-- +goose Down

-- Reverse creation order. Triggers and their functions first: `tasks` survives this
-- rollback, so nothing else would remove them and a re-apply would fail on a function
-- that already exists.
DROP TRIGGER trg_tasks_archive ON tasks;
DROP FUNCTION move_to_archive();
DROP TRIGGER trg_tasks_notify ON tasks;
DROP FUNCTION notify_tasks_available();

-- Indexes on dropped tables go with their table; these two are on `tasks`.
DROP INDEX idx_tasks_lease_reclaim;
DROP INDEX idx_tasks_dequeue;

-- Dropping the partitioned parent drops its partitions; naming them again would fail.
DROP TABLE tasks_archive;
DROP TABLE idempotency_keys;
DROP TABLE schedules;
DROP TABLE dlq_tasks;
DROP TABLE outbox_events;
DROP TABLE task_attempts;

ALTER TABLE tasks DROP CONSTRAINT tasks_status_check;

-- Rollback is dev-only.
CREATE UNIQUE INDEX tasks_idempotency_key_uniq
    ON tasks (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

ALTER TABLE tasks DROP COLUMN trace_id;
ALTER TABLE tasks DROP COLUMN lease_epoch;
ALTER TABLE tasks DROP COLUMN lease_until;
ALTER TABLE tasks DROP COLUMN worker_id;
ALTER TABLE tasks DROP COLUMN started_at;
ALTER TABLE tasks DROP COLUMN updated_at;
ALTER TABLE tasks DROP COLUMN cancel_requested;
ALTER TABLE tasks DROP COLUMN shard_id;
ALTER TABLE tasks DROP COLUMN max_attempts;
ALTER TABLE tasks DROP COLUMN attempt;
ALTER TABLE tasks DROP COLUMN earliest_at;
ALTER TABLE tasks DROP COLUMN priority;
ALTER TABLE tasks DROP COLUMN tenant_id;
