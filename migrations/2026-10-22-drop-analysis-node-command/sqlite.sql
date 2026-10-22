-- Drop the deprecated analysis_nodes.command column (SQLite >= 3.35).
-- The command now lives only in the node's run.sh (analysis_nodes.command_path).
ALTER TABLE analysis_nodes DROP COLUMN command;
