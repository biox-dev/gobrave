-- Rename go_file.role -> go_file.file_key — MySQL.
-- See README.md in this folder for context and the caveats.
-- InnoDB does not roll back DDL, so take a backup first.

-- STEP 1: let AutoMigrate (or the DDL below) add the new column first.
-- MySQL has no ADD COLUMN IF NOT EXISTS: if the column already exists this
-- errors with "Duplicate column name", which is safe to ignore.
-- ALTER TABLE go_file ADD COLUMN file_key varchar(64) NULL;

-- PHASE 1: backfill the key onto the renamed column.
UPDATE go_file
   SET file_key = role
 WHERE (file_key IS NULL OR file_key = '')
   AND role IS NOT NULL
   AND role <> '';

-- PHASE 2 (irreversible): drop the legacy column.
ALTER TABLE go_file DROP COLUMN role;
