-- Add go_dataset_assay.role — PostgreSQL.
-- See README.md in this folder for context and the caveats.

-- STEP 1: let AutoMigrate (or the DDL below) add the new column first.
-- ALTER TABLE go_dataset_assay ADD COLUMN IF NOT EXISTS role varchar(64);

-- No backfill: the role is a new concept for the dataset <-> assay binding.
