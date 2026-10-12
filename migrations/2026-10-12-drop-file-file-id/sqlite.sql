-- Drop the deprecated go_file.file_id business column (SQLite >= 3.35).
ALTER TABLE go_file DROP COLUMN file_id;
