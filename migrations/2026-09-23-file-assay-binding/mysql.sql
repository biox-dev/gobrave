-- go_assay_file is obsolete: a file is assay-private, so the binding lives on
-- go_file (assay_id + role) — MySQL.
-- See README.md in this folder for context and the caveats.
-- InnoDB does not roll back DDL, so take a backup first.

-- STEP 1: let AutoMigrate (or the DDL below) add the new columns first.
-- MySQL has no ADD COLUMN IF NOT EXISTS: if a column already exists this errors
-- with "Duplicate column name", which is safe to ignore.
-- ALTER TABLE go_file ADD COLUMN assay_id bigint NULL;
-- ALTER TABLE go_file ADD COLUMN role varchar(64) NULL;
-- CREATE INDEX idx_go_file_assay_id ON go_file (assay_id);

-- PHASE 1 (optional, recommended): backfill the binding onto the files.
UPDATE go_file f
  JOIN go_assay_file af ON af.file_id = f.id
   SET f.assay_id = af.assay_id,
       f.role     = COALESCE(NULLIF(af.role, ''), f.role);

-- PHASE 2 (irreversible): drop the legacy join table.
DROP TABLE IF EXISTS go_assay_file;
