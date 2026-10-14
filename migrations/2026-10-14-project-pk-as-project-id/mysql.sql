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
-- Section 5 then rewrites the old project segment embedded in
-- nextflow.workspace_dir / analysis_nodes.workspace_dir to the numeric PK, and
-- section 6 does the same for persisted file/path columns (go_file.path,
-- t_file.path, go_container_app_session.workspace_path, agent_tasks.working_dir).
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
-- 5. Rewrite the project segment inside nextflow.workspace_dir and
--    analysis_nodes.workspace_dir.
-- -----------------------------------------------------------------------------
-- The project FK columns above now hold the numeric PK, but the persisted
-- workspace paths still embed the OLD project segment (the removed
-- t_project.project_id): a uuid or the literal `default`, e.g.
--
--   data/<old-key>/analysis/<workflowID>/<analysisID>
--   data/<old-key>/analysis_node/<scriptID>/<nodeID>
--   /home/admin/.brave/analysis/<old-key>/<...>/node-<uuid>   (legacy absolute)
--
-- After the refactor the directory is derived from the numeric PK
-- (utils.GetProjectDir -> data/<int64>), so every workspace_dir must be rewritten
-- from `/<old-key>/` to `/<numeric-PK>/`.
--
-- This must run while t_project.project_id still exists (the old-key -> PK
-- mapping is read from that column here), i.e. before it is dropped. The
-- replacements use the leading+trailing `/seg/` form so only a whole path
-- component is touched, and both the relative (`data/<key>/...`) and legacy
-- absolute (`/.../analysis/<key>/...`) layouts contain `/<key>/`, so a single
-- REPLACE per row covers both.
--
-- The update JOINs t_project on the row's own (already numeric) project_id to
-- recover the old string segment, then swaps it for the numeric PK. It is
-- idempotent: once replaced, `<old-key>` no longer appears, so re-running is a
-- no-op.
--
-- Preview before running this section (expect no old uuid/`default` segments):
--   SELECT project_id, workspace_dir FROM nextflow
--     WHERE workspace_dir REGEXP '(^|/)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|default)(/|$)';

-- 5a. nextflow (analysis) workspace_dir
UPDATE nextflow n
JOIN t_project p ON p.id = n.project_id
SET n.workspace_dir = REPLACE(
        n.workspace_dir,
        CONCAT('/', p.project_id, '/'),
        CONCAT('/', p.id, '/'))
WHERE n.workspace_dir IS NOT NULL
  AND n.workspace_dir <> ''
  AND n.workspace_dir LIKE CONCAT('%/', p.project_id, '/%');

-- 5b. analysis_nodes (node) workspace_dir — project_id may be NULL on legacy
--     rows; those keep `default` and are matched to the `default` project (id 1).
UPDATE analysis_nodes a
JOIN t_project p ON p.id = a.project_id
SET a.workspace_dir = REPLACE(
        a.workspace_dir,
        CONCAT('/', p.project_id, '/'),
        CONCAT('/', p.id, '/'))
WHERE a.workspace_dir IS NOT NULL
  AND a.workspace_dir <> ''
  AND a.workspace_dir LIKE CONCAT('%/', p.project_id, '/%');

-- 5c. Legacy analysis_nodes rows whose project_id is NULL but whose path still
--     carries the `default` segment -> project t_project.id = 1.
UPDATE analysis_nodes a
JOIN t_project p ON p.project_id = 'default'
SET a.workspace_dir = REPLACE(a.workspace_dir, '/default/', CONCAT('/', p.id, '/'))
WHERE (a.project_id IS NULL OR a.project_id = 0)
  AND a.workspace_dir LIKE '%/default/%';

-- Verify (both should return 0):
--   SELECT COUNT(*) FROM nextflow
--     WHERE workspace_dir REGEXP '(^|/)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|default)(/|$)';
--   SELECT COUNT(*) FROM analysis_nodes
--     WHERE workspace_dir REGEXP '(^|/)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|default)(/|$)';

-- -----------------------------------------------------------------------------
-- 6. Rewrite the project segment inside persisted FILE paths (go_file.path,
--    t_file.path) and other project-rooted path columns
--    (go_container_app_session.workspace_path, agent_tasks.working_dir).
-- -----------------------------------------------------------------------------
-- These tables do NOT carry a project_id, so they cannot be joined to a single
-- project row. Instead the update is PROJECT-DRIVEN: for every t_project row we
-- replace its own old key segment with its numeric id wherever that key appears
-- as a whole path component. Because each stored path embeds exactly one
-- project's key (and each old key maps to exactly one project), this rewrites
-- every row to the numeric layout without needing a per-row FK, e.g.
--
--   /home/admin/.brave/data/38db8d90-81f6-4fb0-be5f-202c9584cb2e/analysis/.../plotbb.txt
--   -> /home/admin/.brave/data/2098666223674855424/analysis/.../plotbb.txt
--
-- Requires t_project.project_id to still exist (run BEFORE dropping it).
-- The `/seg/` (leading+trailing slash) form only touches a whole path component,
-- and the JOIN predicate (path LIKE '%/<key>/%') restricts the row to paths that
-- actually contain that project's key, so different projects never collide.
-- Idempotent: once replaced, the old key no longer appears.

-- 6a. go_file.path
UPDATE go_file f
JOIN t_project p ON f.path LIKE CONCAT('%/', p.project_id, '/%')
SET f.path = REPLACE(f.path, CONCAT('/', p.project_id, '/'), CONCAT('/', p.id, '/'));

-- 6b. t_file.path (legacy file table)
UPDATE t_file t
JOIN t_project p ON t.path LIKE CONCAT('%/', p.project_id, '/%')
SET t.path = REPLACE(t.path, CONCAT('/', p.project_id, '/'), CONCAT('/', p.id, '/'));

-- 6c. go_container_app_session.workspace_path
UPDATE go_container_app_session s
JOIN t_project p ON s.workspace_path LIKE CONCAT('%/', p.project_id, '/%')
SET s.workspace_path = REPLACE(s.workspace_path, CONCAT('/', p.project_id, '/'), CONCAT('/', p.id, '/'));

-- 6d. agent_tasks.working_dir
UPDATE agent_tasks a
JOIN t_project p ON a.working_dir LIKE CONCAT('%/', p.project_id, '/%')
SET a.working_dir = REPLACE(a.working_dir, CONCAT('/', p.project_id, '/'), CONCAT('/', p.id, '/'));

-- Verify (each should return 0):
--   SELECT COUNT(*) FROM go_file f JOIN t_project p ON f.path LIKE CONCAT('%/', p.project_id, '/%');
--   SELECT COUNT(*) FROM t_file t JOIN t_project p ON t.path LIKE CONCAT('%/', p.project_id, '/%');
--   SELECT COUNT(*) FROM go_container_app_session s JOIN t_project p ON s.workspace_path LIKE CONCAT('%/', p.project_id, '/%');
--   SELECT COUNT(*) FROM agent_tasks a JOIN t_project p ON a.working_dir LIKE CONCAT('%/', p.project_id, '/%');


