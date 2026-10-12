-- Drop the deprecated go_file.file_id business column (PostgreSQL).
ALTER TABLE go_file DROP COLUMN IF EXISTS file_id;
