-- Range constraints for the two columns whose SQL type is deliberately wider than
-- the domain type mapping onto them (D8, resolved in D1):
--
--   priority SMALLINT (int16) -> tasks.Priority (uint8, 0..MaxPriority=9)
--   shard_id INT      (int32) -> tasks.ShardID  (uint16, 0..65535)
--
-- These are the write-side backstop. tasks.Priority is a defined type over uint8,
-- so Priority(200) in Go bypasses NewPriority entirely; the read side guards the
-- narrowing separately (priorityFromRow / shardIDFromRow).
--
-- shard_id is INT, not SMALLINT: Postgres SMALLINT is *signed* int16 and tops out
-- at 32767, which would silently exclude half the shard space.

-- +goose Up

-- NOT VALID per migration-discipline.md ("Add a CHECK constraint -> NOT VALID +
-- VALIDATE in two migrations"). NOT VALID takes a brief lock and skips the full
-- table scan, so this is safe to run against a hot queue table under load; it
-- still enforces the range on every subsequent INSERT and UPDATE.
--
-- tasks_archive is partitioned, so ADD CONSTRAINT recurses to every partition.

ALTER TABLE tasks
    ADD CONSTRAINT tasks_priority_check CHECK (priority BETWEEN 0 AND 9) NOT VALID;
ALTER TABLE tasks
    ADD CONSTRAINT tasks_shard_id_check CHECK (shard_id BETWEEN 0 AND 65535) NOT VALID;

ALTER TABLE tasks_archive
    ADD CONSTRAINT tasks_archive_priority_check CHECK (priority BETWEEN 0 AND 9) NOT VALID;
ALTER TABLE tasks_archive
    ADD CONSTRAINT tasks_archive_shard_id_check CHECK (shard_id BETWEEN 0 AND 65535) NOT VALID;

-- +goose Down

ALTER TABLE tasks_archive
    DROP CONSTRAINT IF EXISTS tasks_archive_shard_id_check;
ALTER TABLE tasks_archive
    DROP CONSTRAINT IF EXISTS tasks_archive_priority_check;

ALTER TABLE tasks
    DROP CONSTRAINT IF EXISTS tasks_shard_id_check;
ALTER TABLE tasks
    DROP CONSTRAINT IF EXISTS tasks_priority_check;
