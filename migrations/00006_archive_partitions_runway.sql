-- Twelve months of archive partitions. 00003 seeded only two, so from 2026-10-01
-- every finished task landed in tasks_archive_default, the partition that has to
-- stay empty.
--
-- Monthly partitions are meant to be provisioned ahead of the clock by a
-- partition manager, and until one exists, extending this list is a migration.
-- TestArchivePartitions_CoverNextMonth fails a month before the runway ends.
--
-- This fails if tasks_archive_default already holds a row for one of these months:
-- Postgres refuses to attach a partition whose range the default has rows in. A
-- database in that state needs the rows moved first (see the runbook).

-- +goose Up

CREATE TABLE tasks_archive_2026_10 PARTITION OF tasks_archive
    FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
CREATE TABLE tasks_archive_2026_11 PARTITION OF tasks_archive
    FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');
CREATE TABLE tasks_archive_2026_12 PARTITION OF tasks_archive
    FOR VALUES FROM ('2026-12-01') TO ('2027-01-01');
CREATE TABLE tasks_archive_2027_01 PARTITION OF tasks_archive
    FOR VALUES FROM ('2027-01-01') TO ('2027-02-01');
CREATE TABLE tasks_archive_2027_02 PARTITION OF tasks_archive
    FOR VALUES FROM ('2027-02-01') TO ('2027-03-01');
CREATE TABLE tasks_archive_2027_03 PARTITION OF tasks_archive
    FOR VALUES FROM ('2027-03-01') TO ('2027-04-01');
CREATE TABLE tasks_archive_2027_04 PARTITION OF tasks_archive
    FOR VALUES FROM ('2027-04-01') TO ('2027-05-01');
CREATE TABLE tasks_archive_2027_05 PARTITION OF tasks_archive
    FOR VALUES FROM ('2027-05-01') TO ('2027-06-01');
CREATE TABLE tasks_archive_2027_06 PARTITION OF tasks_archive
    FOR VALUES FROM ('2027-06-01') TO ('2027-07-01');
CREATE TABLE tasks_archive_2027_07 PARTITION OF tasks_archive
    FOR VALUES FROM ('2027-07-01') TO ('2027-08-01');
CREATE TABLE tasks_archive_2027_08 PARTITION OF tasks_archive
    FOR VALUES FROM ('2027-08-01') TO ('2027-09-01');
CREATE TABLE tasks_archive_2027_09 PARTITION OF tasks_archive
    FOR VALUES FROM ('2027-09-01') TO ('2027-10-01');

-- +goose Down

-- Dev-only: dropping a partition drops the rows in it.
DROP TABLE tasks_archive_2027_09;
DROP TABLE tasks_archive_2027_08;
DROP TABLE tasks_archive_2027_07;
DROP TABLE tasks_archive_2027_06;
DROP TABLE tasks_archive_2027_05;
DROP TABLE tasks_archive_2027_04;
DROP TABLE tasks_archive_2027_03;
DROP TABLE tasks_archive_2027_02;
DROP TABLE tasks_archive_2027_01;
DROP TABLE tasks_archive_2026_12;
DROP TABLE tasks_archive_2026_11;
DROP TABLE tasks_archive_2026_10;
