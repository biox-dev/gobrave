-- Drop the deprecated go_container_template_spec.work_dir column (SQLite >= 3.35).
-- The container working directory now comes from the runtime variable PROJECT_DIR
-- (DAG node containers still use the node workspace dir).
ALTER TABLE go_container_template_spec DROP COLUMN work_dir;
