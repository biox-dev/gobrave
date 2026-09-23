-- Rename go_file.role -> go_file.file_key — SQLite.
-- See README.md in this folder for context and the caveats.

-- STEP 1: let AutoMigrate add go_file.file_key first.

-- PHASE 1: backfill the key onto the renamed column.
UPDATE go_file
   SET file_key = role
 WHERE (file_key IS NULL OR file_key = '')
   AND role IS NOT NULL
   AND role <> '';

-- PHASE 2 (irreversible): drop the legacy column.
-- DROP COLUMN needs SQLite >= 3.35.
ALTER TABLE go_file DROP COLUMN role;
