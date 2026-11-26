-- Drop the deprecated go_container_template_definition.is_default column (MySQL).
-- There is no longer a "default container template": create_script requires an
-- explicit container_template_id.
ALTER TABLE go_container_template_definition DROP COLUMN is_default;
