-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

CREATE TABLE tasks (
    id UUID PRIMARY KEY,
    idempotency_key TEXT,
    payload JSONB NOT NULL DEFAULT '{}'::JSONB,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX tasks_idempotency_key_uniq
    ON tasks (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- +goose Down
-- pg_stat_statements is cluster-scoped and shared across databases; the Up's
-- CREATE EXTENSION IF NOT EXISTS is intentionally not reversed here. It also
-- requires superuser — see the Phase 4 deployment note.
DROP TABLE tasks;
