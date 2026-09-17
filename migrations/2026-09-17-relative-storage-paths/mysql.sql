-- Relative storage paths — MySQL
-- See README.md in this folder for context. Run inside a transaction if your
-- engine/version supports transactional DDL (InnoDB does not roll back DDL, so
-- take a backup first).

-- ---------------------------------------------------------------------------
-- PHASE 1 (mandatory): backfill the analysis/node roots as base_dir-relative.
-- `data/` is the anchored segment of every workspace path.
-- ---------------------------------------------------------------------------

UPDATE nextflow
SET workspace_dir = CONCAT('data/', SUBSTRING_INDEX(output_dir, '/data/', -1))
WHERE (workspace_dir IS NULL OR workspace_dir = '')
  AND output_dir LIKE '%/data/%';

UPDATE analysis_nodes
SET workspace_dir = CONCAT('data/', SUBSTRING_INDEX(workspace_dir, '/data/', -1))
WHERE workspace_dir LIKE '/%'
  AND workspace_dir LIKE '%/data/%';

-- ---------------------------------------------------------------------------
-- PHASE 2 (optional): drop the columns that are no longer mapped.
-- ---------------------------------------------------------------------------

ALTER TABLE nextflow
  DROP COLUMN work_dir,
  DROP COLUMN output_dir,
  DROP COLUMN params_path,
  DROP COLUMN command_path,
  DROP COLUMN command_log_path,
  DROP COLUMN trace_file,
  DROP COLUMN workflow_log_file,
  DROP COLUMN executor_log_file;

ALTER TABLE analysis_nodes
  DROP COLUMN output_dir,
  DROP COLUMN cache_dir,
  DROP COLUMN command_path,
  DROP COLUMN params_path,
  DROP COLUMN log_path;

ALTER TABLE store
  DROP COLUMN path;
