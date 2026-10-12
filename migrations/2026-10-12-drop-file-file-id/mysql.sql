-- Drop the deprecated go_file.file_id business column (MySQL).
-- The unique index on the column is dropped together with it.
ALTER TABLE go_file DROP COLUMN file_id;
