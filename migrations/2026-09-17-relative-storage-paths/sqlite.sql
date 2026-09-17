-- Relative storage paths — SQLite (requires 3.35+ for DROP COLUMN)
-- See README.md in this folder for context.

BEGIN;

-- ---------------------------------------------------------------------------
-- PHASE 1 (mandatory): backfill the analysis/node roots as base_dir-relative.
-- `data/` is the anchored segment of every workspace path.
-- ---------------------------------------------------------------------------

UPDATE nextflow
SET workspace_dir = 'data/' || substr(output_dir, instr(output_dir, '/data/') + 6)
WHERE (workspace_dir IS NULL OR workspace_dir = '')
  AND output_dir LIKE '%/data/%';

UPDATE analysis_nodes
SET workspace_dir = 'data/' || substr(workspace_dir, instr(workspace_dir, '/data/') + 6)
WHERE workspace_dir LIKE '/%'
  AND workspace_dir LIKE '%/data/%';

-- ---------------------------------------------------------------------------
-- PHASE 2 (optional): drop the columns that are no longer mapped.
-- ---------------------------------------------------------------------------

ALTER TABLE nextflow DROP COLUMN work_dir;
ALTER TABLE nextflow DROP COLUMN output_dir;
ALTER TABLE nextflow DROP COLUMN params_path;
ALTER TABLE nextflow DROP COLUMN command_path;
ALTER TABLE nextflow DROP COLUMN command_log_path;
ALTER TABLE nextflow DROP COLUMN trace_file;
ALTER TABLE nextflow DROP COLUMN workflow_log_file;
ALTER TABLE nextflow DROP COLUMN executor_log_file;

ALTER TABLE analysis_nodes DROP COLUMN output_dir;
ALTER TABLE analysis_nodes DROP COLUMN cache_dir;
ALTER TABLE analysis_nodes DROP COLUMN command_path;
ALTER TABLE analysis_nodes DROP COLUMN params_path;
ALTER TABLE analysis_nodes DROP COLUMN log_path;

ALTER TABLE store DROP COLUMN path;

COMMIT;
