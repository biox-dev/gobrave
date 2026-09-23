-- go_subject.subject_id -> go_subject.subject_name — SQLite
-- See README.md in this folder for context.
--
-- SQLite has no conditional DDL, so this file cannot be made idempotent:
-- run it only against a database that still has go_subject.subject_id. A fresh
-- database created by AutoMigrate already has the new column and must NOT run
-- this file. Requires SQLite >= 3.25 (RENAME COLUMN); the Go driver ships newer.

ALTER TABLE go_subject RENAME COLUMN subject_id TO subject_name;

-- The renamed column keeps its old index definition; recreate it under the name
-- GORM expects.
DROP INDEX IF EXISTS idx_go_subject_subject_id;
CREATE UNIQUE INDEX IF NOT EXISTS idx_go_subject_subject_name ON go_subject (subject_name);
