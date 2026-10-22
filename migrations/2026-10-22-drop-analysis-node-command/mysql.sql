-- Drop the deprecated analysis_nodes.command column (MySQL).
-- The command now lives only in the node's run.sh (analysis_nodes.command_path).
ALTER TABLE analysis_nodes DROP COLUMN command;
