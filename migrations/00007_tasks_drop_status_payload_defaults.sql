-- 00001 gave tasks.status and tasks.payload column defaults, and 00003's rule is
-- that an INSERT which forgets a column must fail rather than silently get a
-- default forever. CreateTask now passes both from the aggregate, so neither
-- default has a reader left.
--
-- DROP DEFAULT rewrites nothing: it is a catalog change under a brief lock.

-- +goose Up

ALTER TABLE tasks ALTER COLUMN status DROP DEFAULT;
ALTER TABLE tasks ALTER COLUMN payload DROP DEFAULT;

-- +goose Down

ALTER TABLE tasks ALTER COLUMN payload SET DEFAULT '{}'::JSONB;
ALTER TABLE tasks ALTER COLUMN status SET DEFAULT 'pending';
