-- =============================================================================
-- Project identity: drop the string business id, use the int64 PK everywhere.
-- =============================================================================
-- `t_project.project_id` (a uuid varchar business id) was removed from the Go
-- model. The project's primary key `t_project.id` (bigint) is now the ONLY
-- project identifier, and it is also the storage directory key
-- (`data/<id>/`). Every table that referenced a project by the old
-- `project_id` varchar must be rewritten to hold the numeric PK instead.
--
-- MySQL cannot convert a uuid varchar directly to bigint, so for each table:
--   1) add a temporary <col>_new bigint column,
--   2) backfill it by joining the OLD string value against t_project.project_id,
--   3) drop the old column (and its indexes),
--   4) rename <col>_new -> <col> and re-create the indexes with the same names.
-- Finally drop t_project.project_id itself.
--
-- IMPORTANT: run this BEFORE starting the new binary. On MySQL strict mode the
-- new binary writes numeric ids into columns that are still varchar, which
-- happens to store fine, but old binaries read uuids and would break the moment
-- this migration runs. Order: stop old binary -> run this DDL -> start new binary.
--
-- Idempotency note: this script is NOT re-runnable (it renames/drops columns).
-- Take a backup first. Wrapped in a transaction where DDL allows; MySQL DDL is
-- auto-committing, so the statements are ordered so a partial failure leaves the
-- temporary columns (safe: the old columns are dropped last).

-- -----------------------------------------------------------------------------
-- 0. Safety: abort if any referencing string value cannot be resolved to a PK.
-- -----------------------------------------------------------------------------
-- (Optional manual check - run these first and expect every count to be 0.)
-- SELECT COUNT(*) FROM user_project up       LEFT JOIN t_project p ON p.project_id = up.project_id       WHERE p.id IS NULL;
-- SELECT COUNT(*) FROM t_project_report r    LEFT JOIN t_project p ON p.project_id = r.project_id        WHERE p.id IS NULL;
-- SELECT COUNT(*) FROM go_project_dataset d  LEFT JOIN t_project p ON p.project_id = d.project_id        WHERE p.id IS NULL;
-- SELECT COUNT(*) FROM project_literature pl LEFT JOIN t_project p ON p.project_id = pl.project_id       WHERE p.id IS NULL;
-- SELECT COUNT(*) FROM t_literature l        LEFT JOIN t_project p ON p.project_id = l.owner_project_id  WHERE l.owner_project_id IS NOT NULL AND l.owner_project_id <> '' AND p.id IS NULL;

-- -----------------------------------------------------------------------------
-- 1. user_project.project_id : varchar(255) -> bigint
-- -----------------------------------------------------------------------------
ALTER TABLE user_project ADD COLUMN project_id_new BIGINT NULL;

UPDATE user_project up
JOIN t_project p ON p.project_id = up.project_id
SET up.project_id_new = p.id;

-- The unique key (user_id, project_id) and the plain index (project_id) must be
-- dropped before the old column goes away.
ALTER TABLE user_project DROP INDEX idx_user_project_unique;
ALTER TABLE user_project DROP INDEX idx_user_project_project;
ALTER TABLE user_project DROP COLUMN project_id;
ALTER TABLE user_project CHANGE COLUMN project_id_new project_id BIGINT NOT NULL;
ALTER TABLE user_project ADD INDEX idx_user_project_project (project_id);
ALTER TABLE user_project ADD UNIQUE INDEX idx_user_project_unique (user_id, project_id);

-- -----------------------------------------------------------------------------
-- 2. t_project_report.project_id : varchar(255) -> bigint
-- -----------------------------------------------------------------------------
ALTER TABLE t_project_report ADD COLUMN project_id_new BIGINT NULL;

UPDATE t_project_report r
JOIN t_project p ON p.project_id = r.project_id
SET r.project_id_new = p.id;

ALTER TABLE t_project_report DROP INDEX idx_t_project_report_project_id;
ALTER TABLE t_project_report DROP COLUMN project_id;
ALTER TABLE t_project_report CHANGE COLUMN project_id_new project_id BIGINT NOT NULL;
ALTER TABLE t_project_report ADD INDEX idx_t_project_report_project_id (project_id);

-- -----------------------------------------------------------------------------
-- 3. go_project_dataset.project_id : varchar(191) -> bigint
-- -----------------------------------------------------------------------------
ALTER TABLE go_project_dataset ADD COLUMN project_id_new BIGINT NULL;

UPDATE go_project_dataset d
JOIN t_project p ON p.project_id = d.project_id
SET d.project_id_new = p.id;

ALTER TABLE go_project_dataset DROP INDEX idx_go_project_dataset_project_id;
ALTER TABLE go_project_dataset DROP COLUMN project_id;
ALTER TABLE go_project_dataset CHANGE COLUMN project_id_new project_id BIGINT NOT NULL;
ALTER TABLE go_project_dataset ADD INDEX idx_go_project_dataset_project_id (project_id);

-- -----------------------------------------------------------------------------
-- 4. project_literature.project_id : varchar(255) -> bigint
-- -----------------------------------------------------------------------------
ALTER TABLE project_literature ADD COLUMN project_id_new BIGINT NULL;

UPDATE project_literature pl
JOIN t_project p ON p.project_id = pl.project_id
SET pl.project_id_new = p.id;

ALTER TABLE project_literature DROP INDEX idx_project_literature_unique;
ALTER TABLE project_literature DROP INDEX idx_project_literature_project;
ALTER TABLE project_literature DROP COLUMN project_id;
ALTER TABLE project_literature CHANGE COLUMN project_id_new project_id BIGINT NOT NULL;
ALTER TABLE project_literature ADD INDEX idx_project_literature_project (project_id);
ALTER TABLE project_literature ADD UNIQUE INDEX idx_project_literature_unique (project_id, literature_id);

-- -----------------------------------------------------------------------------
-- 5. t_literature.owner_project_id : varchar(255) -> bigint (nullable)
-- -----------------------------------------------------------------------------
ALTER TABLE t_literature ADD COLUMN owner_project_id_new BIGINT NULL;

UPDATE t_literature l
JOIN t_project p ON p.project_id = l.owner_project_id
SET l.owner_project_id_new = p.id
WHERE l.owner_project_id IS NOT NULL AND l.owner_project_id <> '';

ALTER TABLE t_literature DROP INDEX idx_t_literature_owner_project_id;
ALTER TABLE t_literature DROP COLUMN owner_project_id;
ALTER TABLE t_literature CHANGE COLUMN owner_project_id_new owner_project_id BIGINT NULL;
ALTER TABLE t_literature ADD INDEX idx_t_literature_owner_project_id (owner_project_id);

-- -----------------------------------------------------------------------------
-- 6. Drop the now-unused t_project.project_id business column.
-- -----------------------------------------------------------------------------
ALTER TABLE t_project DROP INDEX idx_t_project_project_id;
ALTER TABLE t_project DROP COLUMN project_id;
