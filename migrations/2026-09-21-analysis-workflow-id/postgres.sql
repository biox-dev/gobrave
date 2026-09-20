-- nextflow.relation_id -> nextflow.workflow_id (workflow PK) — PostgreSQL
-- See README.md in this folder for context and the caveats.

-- ---------------------------------------------------------------------------
-- PHASE 1 (mandatory): add + backfill workflow_id and index it.
-- IF NOT EXISTS makes 1a/1c safe even if AutoMigrate already created them.
-- ---------------------------------------------------------------------------

-- 1a. new column
ALTER TABLE nextflow ADD COLUMN IF NOT EXISTS workflow_id BIGINT;

-- 1b. backfill from the workflow table.
--     relation_id alone is NOT unique (one row per project that installed the
--     workflow), so the join must also match project_id.
UPDATE nextflow n
SET workflow_id = r.id
FROM pipeline_components_relation r
WHERE r.relation_id = n.relation_id
  AND r.project_id  = n.project_id
  AND n.relation_id IS NOT NULL
  AND n.relation_id <> '';

-- 1c. index for the workflow_id filters (ListAnalysisByWorkflowID / list-by-project)
CREATE INDEX IF NOT EXISTS idx_nextflow_workflow_id ON nextflow (workflow_id);

-- sanity check: should print 0 rows on a healthy database
-- SELECT id, project_id, relation_id FROM nextflow WHERE workflow_id IS NULL;

-- ---------------------------------------------------------------------------
-- PHASE 2 (optional, irreversible): drop the legacy column.
-- ---------------------------------------------------------------------------

-- ALTER TABLE nextflow DROP COLUMN relation_id;
