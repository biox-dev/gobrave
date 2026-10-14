-- =============================================================================
-- Rewrite the project segment inside nextflow.workspace_dir / analysis_nodes.workspace_dir
-- =============================================================================
-- Companion to 2026-10-14-project-pk-as-project-id (which flipped the project
-- FK columns to the numeric PK and dropped t_project.project_id).
--
-- That migration changed the *keys* used by the code, but the on-disk / persisted
-- `workspace_dir` strings still embed the OLD project segment:
--
--   data/<old-key>/analysis/<workflowID>/<analysisID>
--   data/<old-key>/analysis_node/<scriptID>/<nodeID>
--   /home/admin/.brave/analysis/<old-key>/<...>/node-<uuid>   (legacy absolute)
--
-- where <old-key> is the removed t_project.project_id: a uuid
-- (e.g. 38db8d90-81f6-4fb0-be5f-202c9584cb2e) or the literal `default`.
--
-- After the refactor the code derives the project directory from the numeric PK
-- (utils.GetProjectDir -> data/<int64>), so every persisted workspace_dir must be
-- rewritten from `/<old-key>/` to `/<numeric-PK>/`.
--
-- The mapping is NOT available via a JOIN anymore (t_project.project_id is
-- already dropped), so it is recovered from the data itself. Each old segment
-- resolves to exactly one numeric project_id; the explicit statements below are
-- generated from:
--
--   SELECT project_id, SUBSTRING_INDEX(SUBSTRING_INDEX(workspace_dir,'/',2),'/',-1) seg
--   FROM nextflow WHERE workspace_dir LIKE 'data/%'
--   GROUP BY project_id, seg;
--
-- Verified on the live DB (before running):
--   38db8d90-81f6-4fb0-be5f-202c9584cb2e -> 2098666223674855424  (nextflow 76, nodes 220+)
--   d9a2f545-09d9-4066-a800-2b07d231cf56 -> 2                    (nextflow 1)
--   default                              -> 1                    (nextflow 7, nodes 47)
-- Every segment maps to a single PK (no segment is shared by two projects), so
-- the replacements below cannot rewrite another project's rows.
--
-- REPLACE with the `/seg/` form (leading+trailing slash) only touches a whole
-- path component, never a partial substring elsewhere in the path.
--
-- Order: run AFTER 2026-10-14-project-pk-as-project-id. It is idempotent
-- (running it twice is a no-op because `<old-key>` no longer appears once
-- replaced). Back up first.

-- -----------------------------------------------------------------------------
-- 0. Preview / pre-check (run these first; nothing below should error on them).
-- -----------------------------------------------------------------------------
-- SELECT DISTINCT SUBSTRING_INDEX(SUBSTRING_INDEX(workspace_dir,'/',2),'/',-1) AS seg, project_id
-- FROM nextflow WHERE workspace_dir LIKE 'data/%';
-- SELECT DISTINCT SUBSTRING_INDEX(SUBSTRING_INDEX(workspace_dir,'/',2),'/',-1) AS seg, project_id
-- FROM analysis_nodes WHERE workspace_dir LIKE 'data/%';

-- -----------------------------------------------------------------------------
-- 1. nextflow (analysis) workspace_dir
-- -----------------------------------------------------------------------------
-- default -> 1
UPDATE nextflow
SET workspace_dir = REPLACE(workspace_dir, '/default/', '/1/')
WHERE workspace_dir LIKE 'data/default/%';

-- d9a2f545-09d9-4066-a800-2b07d231cf56 -> 2
UPDATE nextflow
SET workspace_dir = REPLACE(workspace_dir, '/d9a2f545-09d9-4066-a800-2b07d231cf56/', '/2/')
WHERE workspace_dir LIKE 'data/d9a2f545-09d9-4066-a800-2b07d231cf56/%';

-- 38db8d90-81f6-4fb0-be5f-202c9584cb2e -> 2098666223674855424
UPDATE nextflow
SET workspace_dir = REPLACE(workspace_dir, '/38db8d90-81f6-4fb0-be5f-202c9584cb2e/', '/2098666223674855424/')
WHERE workspace_dir LIKE 'data/38db8d90-81f6-4fb0-be5f-202c9584cb2e/%';

-- -----------------------------------------------------------------------------
-- 2. analysis_nodes (node) workspace_dir
--    Two layouts: relative data/<seg>/... and legacy absolute
--    /home/admin/.brave/analysis/<seg>/... ; both contain `/<seg>/`.
-- -----------------------------------------------------------------------------
-- default -> 1
UPDATE analysis_nodes
SET workspace_dir = REPLACE(workspace_dir, '/default/', '/1/')
WHERE workspace_dir LIKE 'data/default/%'
   OR workspace_dir LIKE '/%/analysis/default/%';

-- d9a2f545-09d9-4066-a800-2b07d231cf56 -> 2
UPDATE analysis_nodes
SET workspace_dir = REPLACE(workspace_dir, '/d9a2f545-09d9-4066-a800-2b07d231cf56/', '/2/')
WHERE workspace_dir LIKE 'data/d9a2f545-09d9-4066-a800-2b07d231cf56/%'
   OR workspace_dir LIKE '/%/analysis/d9a2f545-09d9-4066-a800-2b07d231cf56/%';

-- 38db8d90-81f6-4fb0-be5f-202c9584cb2e -> 2098666223674855424
UPDATE analysis_nodes
SET workspace_dir = REPLACE(workspace_dir, '/38db8d90-81f6-4fb0-be5f-202c9584cb2e/', '/2098666223674855424/')
WHERE workspace_dir LIKE 'data/38db8d90-81f6-4fb0-be5f-202c9584cb2e/%'
   OR workspace_dir LIKE '/%/analysis/38db8d90-81f6-4fb0-be5f-202c9584cb2e/%';

-- -----------------------------------------------------------------------------
-- 3. Verify: no old segment should remain in either table.
-- -----------------------------------------------------------------------------
-- SELECT COUNT(*) FROM nextflow
--   WHERE workspace_dir REGEXP '(^|/)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|default)(/|$)';
-- SELECT COUNT(*) FROM analysis_nodes
--   WHERE workspace_dir REGEXP '(^|/)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|default)(/|$)';
