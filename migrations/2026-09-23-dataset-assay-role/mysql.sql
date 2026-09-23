-- Add go_dataset_assay.role — MySQL.
-- See README.md in this folder for context and the caveats.

-- STEP 1: let AutoMigrate (or the DDL below) add the new column first.
-- MySQL has no ADD COLUMN IF NOT EXISTS: if the column already exists this
-- errors with "Duplicate column name", which is safe to ignore.
-- ALTER TABLE go_dataset_assay ADD COLUMN role varchar(64) NULL;

-- No backfill: the role is a new concept for the dataset <-> assay binding.
