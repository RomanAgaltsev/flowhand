-- Second half of the NOT VALID + VALIDATE pair from 00004, per
-- migration-discipline.md. VALIDATE CONSTRAINT scans the table to prove the
-- existing rows satisfy the range, but takes only SHARE UPDATE EXCLUSIVE — it
-- does not block reads or writes, unlike a plain ADD CONSTRAINT.
--
-- Split into its own migration so the scan can be deferred to a quiet window on
-- a large table without holding up the deploy that adds the constraint.

-- +goose Up

ALTER TABLE tasks VALIDATE CONSTRAINT tasks_priority_check;
ALTER TABLE tasks VALIDATE CONSTRAINT tasks_shard_id_check;

ALTER TABLE tasks_archive VALIDATE CONSTRAINT tasks_archive_priority_check;
ALTER TABLE tasks_archive VALIDATE CONSTRAINT tasks_archive_shard_id_check;

-- +goose Down

-- There is no "un-validate". Rolling back to NOT VALID means dropping and
-- re-adding the constraint, which is exactly what 00004's Down does; this Down
-- is therefore a no-op and 00004 is the rollback boundary.

SELECT 1;
