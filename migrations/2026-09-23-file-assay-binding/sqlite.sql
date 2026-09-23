-- go_assay_file is obsolete: a file is assay-private, so the binding lives on
-- go_file (assay_id + role) — SQLite.
-- See README.md in this folder for context and the caveats.

-- STEP 1: let AutoMigrate add go_file.assay_id / go_file.role first; AutoMigrate
-- also creates the idx_go_file_assay_id index.

-- PHASE 1 (optional, recommended): backfill the binding onto the files.
UPDATE go_file
   SET assay_id = (
         SELECT af.assay_id FROM go_assay_file AS af WHERE af.file_id = go_file.id
       ),
       role = COALESCE(
         (SELECT NULLIF(af.role, '') FROM go_assay_file AS af WHERE af.file_id = go_file.id),
         role
       )
 WHERE EXISTS (SELECT 1 FROM go_assay_file AS af WHERE af.file_id = go_file.id);

-- PHASE 2 (irreversible): drop the legacy join table.
DROP TABLE IF EXISTS go_assay_file;
