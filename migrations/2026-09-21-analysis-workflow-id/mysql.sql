-- nextflow.relation_id -> nextflow.workflow_id (workflow PK) — MySQL
-- See README.md in this folder for context and the caveats.
-- InnoDB does not roll back DDL, so take a backup first.

-- ---------------------------------------------------------------------------
-- PHASE 1 (mandatory): add + backfill workflow_id and index it.
-- Skip 1a/1c if the new binary already ran AutoMigrate (it creates both).
-- ---------------------------------------------------------------------------

-- 1a. new column
ALTER TABLE nextflow ADD COLUMN workflow_id BIGINT NULL;

-- 1b. backfill from the workflow table.
--     relation_id alone is NOT unique (one row per project that installed the
--     workflow), so the join must also match project_id.
UPDATE nextflow n
  JOIN pipeline_components_relation r
    ON r.relation_id = n.relation_id
   AND r.project_id  = n.project_id
SET n.workflow_id = r.id
WHERE n.relation_id IS NOT NULL
  AND n.relation_id <> '';

-- 1c. index for the workflow_id filters (ListAnalysisByWorkflowID / list-by-project)
CREATE INDEX idx_nextflow_workflow_id ON nextflow (workflow_id);

-- sanity check: should print 0 rows on a healthy database
-- SELECT id, project_id, relation_id FROM nextflow WHERE workflow_id IS NULL;

-- ---------------------------------------------------------------------------
-- PHASE 2 (optional, irreversible): drop the legacy column.
-- Run only after the new binary has been validated against the migrated data.
-- ---------------------------------------------------------------------------

-- ALTER TABLE nextflow DROP COLUMN relation_id;
