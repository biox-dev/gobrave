-- Drop the persisted workspace directories (MySQL).
-- Directories are now derived from storage.base_dir + the row's own keys
-- (types.AnalysisWorkflow.Dir / types.AnalysisNode.Dir).
ALTER TABLE nextflow DROP COLUMN workspace_dir;
ALTER TABLE analysis_nodes DROP COLUMN workspace_dir;
