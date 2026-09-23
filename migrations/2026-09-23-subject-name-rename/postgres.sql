-- go_subject.subject_id -> go_subject.subject_name — PostgreSQL
-- See README.md in this folder for context and the ordering requirement:
-- run this BEFORE starting the renamed binary (AutoMigrate cannot rename).

-- ---------------------------------------------------------------------------
-- PHASE 1 (the actual migration) — both blocks are no-ops when already applied
-- ---------------------------------------------------------------------------

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns
              WHERE table_schema = current_schema()
                AND table_name = 'go_subject'
                AND column_name = 'subject_id')
     AND NOT EXISTS (SELECT 1 FROM information_schema.columns
                      WHERE table_schema = current_schema()
                        AND table_name = 'go_subject'
                        AND column_name = 'subject_name') THEN
    ALTER TABLE go_subject RENAME COLUMN subject_id TO subject_name;
  END IF;
END $$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'idx_go_subject_subject_id')
     AND NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'idx_go_subject_subject_name') THEN
    ALTER INDEX idx_go_subject_subject_id RENAME TO idx_go_subject_subject_name;
  END IF;
END $$;

-- ---------------------------------------------------------------------------
-- PHASE 2 (recovery only): the new binary already ran AutoMigrate, so both
-- columns exist and PHASE 1 skipped. Copy the values across first.
-- ---------------------------------------------------------------------------
-- UPDATE go_subject SET subject_name = subject_id
--  WHERE (subject_name IS NULL OR subject_name = '') AND subject_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- PHASE 3 (recovery only, irreversible): drop the legacy column.
-- ---------------------------------------------------------------------------
-- ALTER TABLE go_subject DROP COLUMN IF EXISTS subject_id;

-- ---------------------------------------------------------------------------
-- Verification
-- ---------------------------------------------------------------------------
-- SELECT column_name, data_type FROM information_schema.columns
--  WHERE table_schema = current_schema() AND table_name = 'go_subject'
--  ORDER BY ordinal_position;
