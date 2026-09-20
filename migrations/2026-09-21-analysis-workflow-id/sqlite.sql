-- nextflow.relation_id -> nextflow.workflow_id (workflow PK) — SQLite
-- See README.md in this folder for context and the caveats.
-- (uses the correlated-subquery form of UPDATE so it works on older SQLite too)

-- ---------------------------------------------------------------------------
-- PHASE 1 (mandatory): add + backfill workflow_id and index it.
-- Skip the two DDL statements if the new binary already ran AutoMigrate.
-- ---------------------------------------------------------------------------

-- 1a. new column
ALTER TABLE nextflow ADD COLUMN workflow_id INTEGER;

-- 1b. backfill from the workflow table.
--     relation_id alone is NOT unique (one row per project that installed the
--     workflow), so the lookup must also match project_id. The scalar subquery
--     returns NULL when nothing matches, which is the desired "unmapped" value.
UPDATE nextflow
SET workflow_id = (
      SELECT r.id
        FROM pipeline_components_relation r
       WHERE r.relation_id = nextflow.relation_id
         AND r.project_id  = nextflow.project_id
    )
WHERE relation_id IS NOT NULL
  AND relation_id <> '';

-- 1c. index for the workflow_id filters (ListAnalysisByWorkflowID / list-by-project)
CREATE INDEX IF NOT EXISTS idx_nextflow_workflow_id ON nextflow (workflow_id);

-- sanity check: should print 0 rows on a healthy database
-- SELECT id, project_id, relation_id FROM nextflow WHERE workflow_id IS NULL;

-- ---------------------------------------------------------------------------
-- PHASE 2 (optional, irreversible): drop the legacy column.
-- SQLite supports DROP COLUMN since 3.35.
-- ---------------------------------------------------------------------------

-- ALTER TABLE nextflow DROP COLUMN relation_id;
