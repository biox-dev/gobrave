-- Drop the persisted workspace directories (SQLite).
-- SQLite supports DROP COLUMN since 3.35.
ALTER TABLE nextflow DROP COLUMN workspace_dir;
ALTER TABLE analysis_nodes DROP COLUMN workspace_dir;
