-- Container template split: go_container_template -> spec + definition
-- PostgreSQL. See README.md in this folder for context and verification queries.
--
-- PREREQUISITE: start the new binary once so AutoMigrate creates
--   go_container_template_spec and go_container_template_definition.
-- Safe to run inside a transaction; both statements are idempotent.

BEGIN;

-- ---------------------------------------------------------------------------
-- PHASE 1 (mandatory): one old row -> one spec row + one definition row.
-- definition.id == old template id, so the three reference columns
-- (pipeline_components.container_template_id,
--  go_container_app_session.container_template_id,
--  go_container_instance.template_id) keep resolving unchanged.
-- ---------------------------------------------------------------------------

-- 1a. "how to run" -> go_container_template_spec
INSERT INTO go_container_template_spec
  (id, name, description, command, cpu, memory, work_dir, port, app_type,
   env, mounts, scheduling_constraint, labels, change_uid, created_at, updated_at)
SELECT
   id, name, description, command, cpu, memory, work_dir, port, app_type,
   env, mounts, scheduling_constraint, labels, change_uid, created_at, updated_at
  FROM go_container_template
ON CONFLICT (id) DO NOTHING;

-- 1b. "what to run" -> go_container_template_definition
INSERT INTO go_container_template_definition
  (id, spec_id, image_id, display_name,
   r_library_path, python_library_path, conda_library_path, created_at, updated_at)
SELECT
   id, id, image_id, NULL,
   r_library_path, python_library_path, conda_library_path, created_at, updated_at
  FROM go_container_template
ON CONFLICT (id) DO NOTHING;

COMMIT;

-- ---------------------------------------------------------------------------
-- PHASE 2 (optional, irreversible): drop the legacy table.
-- ---------------------------------------------------------------------------

-- DROP TABLE go_container_template;
