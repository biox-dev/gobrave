-- go_subject.subject_id -> go_subject.subject_name — MySQL
-- See README.md in this folder for context and the ordering requirement:
-- run this BEFORE starting the renamed binary (AutoMigrate cannot rename).
-- Idempotent: every statement is guarded, so re-running is a no-op.
-- InnoDB does not roll back DDL, so take a backup first.

-- ---------------------------------------------------------------------------
-- PHASE 1 (the actual migration)
-- ---------------------------------------------------------------------------

-- 1a) column rename. Only fires when the old column exists and the new one does
--     not; CHANGE COLUMN keeps the NOT NULL flag and the data in place.
SET @has_old := (SELECT COUNT(*) FROM information_schema.COLUMNS
                 WHERE TABLE_SCHEMA = DATABASE()
                   AND TABLE_NAME = 'go_subject'
                   AND COLUMN_NAME = 'subject_id');
SET @has_new := (SELECT COUNT(*) FROM information_schema.COLUMNS
                 WHERE TABLE_SCHEMA = DATABASE()
                   AND TABLE_NAME = 'go_subject'
                   AND COLUMN_NAME = 'subject_name');
SET @sql := IF(@has_old = 1 AND @has_new = 0,
  'ALTER TABLE go_subject CHANGE COLUMN subject_id subject_name VARCHAR(255) NOT NULL',
  'DO 0');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 1b) unique index rename (cosmetic: GORM expects idx_go_subject_subject_name).
SET @has_old_ix := (SELECT COUNT(*) FROM information_schema.STATISTICS
                    WHERE TABLE_SCHEMA = DATABASE()
                      AND TABLE_NAME = 'go_subject'
                      AND INDEX_NAME = 'idx_go_subject_subject_id');
SET @has_new_ix := (SELECT COUNT(*) FROM information_schema.STATISTICS
                    WHERE TABLE_SCHEMA = DATABASE()
                      AND TABLE_NAME = 'go_subject'
                      AND INDEX_NAME = 'idx_go_subject_subject_name');
SET @sql := IF(@has_old_ix > 0 AND @has_new_ix = 0,
  'ALTER TABLE go_subject RENAME INDEX idx_go_subject_subject_id TO idx_go_subject_subject_name',
  'DO 0');
PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ---------------------------------------------------------------------------
-- PHASE 2 (recovery only): the new binary already ran AutoMigrate, so both
-- columns exist and PHASE 1 skipped. Copy the values across first.
-- ---------------------------------------------------------------------------
-- UPDATE go_subject SET subject_name = subject_id
--  WHERE (subject_name IS NULL OR subject_name = '') AND subject_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- PHASE 3 (recovery only, irreversible): drop the legacy column and rename the
-- stale unique index left behind by PHASE 1.
-- ---------------------------------------------------------------------------
-- ALTER TABLE go_subject DROP INDEX IF EXISTS idx_go_subject_subject_id;
-- ALTER TABLE go_subject DROP COLUMN subject_id;

-- ---------------------------------------------------------------------------
-- Verification (expected: subject_name only, PK id, unique idx_go_subject_subject_name)
-- ---------------------------------------------------------------------------
-- SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY
--   FROM information_schema.COLUMNS
--  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'go_subject'
--  ORDER BY ORDINAL_POSITION;
-- SELECT INDEX_NAME, COLUMN_NAME, NON_UNIQUE
--   FROM information_schema.STATISTICS
--  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'go_subject';
