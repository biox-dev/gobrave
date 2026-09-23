-- go_assay_file is obsolete: a file is assay-private, so the binding lives on
-- go_file (assay_id + role) — PostgreSQL.
-- See README.md in this folder for context and the caveats.

-- STEP 1: let AutoMigrate (or the DDL below) add the new columns first.
-- ALTER TABLE go_file ADD COLUMN IF NOT EXISTS assay_id bigint;
-- ALTER TABLE go_file ADD COLUMN IF NOT EXISTS role varchar(64);
-- CREATE INDEX IF NOT EXISTS idx_go_file_assay_id ON go_file (assay_id);

-- PHASE 1 (optional, recommended): backfill the binding onto the files.
UPDATE go_file AS f
   SET assay_id = af.assay_id,
       role     = COALESCE(NULLIF(af.role, ''), f.role)
  FROM go_assay_file AS af
 WHERE af.file_id = f.id;

-- PHASE 2 (irreversible): drop the legacy join table.
DROP TABLE IF EXISTS go_assay_file;
