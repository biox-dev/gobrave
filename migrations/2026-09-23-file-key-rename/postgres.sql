-- Rename go_file.role -> go_file.file_key — PostgreSQL.
-- See README.md in this folder for context and the caveats.

-- STEP 1: let AutoMigrate (or the DDL below) add the new column first.
-- ALTER TABLE go_file ADD COLUMN IF NOT EXISTS file_key varchar(64);

-- PHASE 1: backfill the key onto the renamed column.
UPDATE go_file
   SET file_key = role
 WHERE (file_key IS NULL OR file_key = '')
   AND role IS NOT NULL
   AND role <> '';

-- PHASE 2 (irreversible): drop the legacy column.
ALTER TABLE go_file DROP COLUMN role;
