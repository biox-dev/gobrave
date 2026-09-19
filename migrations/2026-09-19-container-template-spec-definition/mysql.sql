-- Container template split: go_container_template -> spec + definition
-- MySQL. See README.md in this folder for context and the verification queries.
--
-- PREREQUISITE: start the new binary once so AutoMigrate creates
--   go_container_template_spec and go_container_template_definition.
-- InnoDB does not roll back DDL, but both statements below are DML and
-- idempotent (INSERT IGNORE on the primary key), so re-running is safe.
-- Take a backup anyway.

-- ---------------------------------------------------------------------------
-- PHASE 1 (mandatory): fan one old template row out into
--   * one spec row       (how to run; spec.id reuses the template id)
--   * one definition row (spec x image binding; definition.id == template id)
-- Keeping definition.id == old template id leaves
--   pipeline_components.container_template_id,
--   go_container_app_session.container_template_id and
--   go_container_instance.template_id pointing at the same value.
-- ---------------------------------------------------------------------------

-- 1a. "how to run" -> go_container_template_spec
INSERT IGNORE INTO go_container_template_spec
  (id, name, description, command, cpu, memory, work_dir, port, app_type,
   env, mounts, scheduling_constraint, labels, change_uid, created_at, updated_at)
SELECT
   id, name, description, command, cpu, memory, work_dir, port, app_type,
   env, mounts, scheduling_constraint, labels, change_uid, created_at, updated_at
  FROM go_container_template;

-- 1b. "what to run" -> go_container_template_definition
--     display_name stays NULL on purpose: the read model then uses the spec name,
--     so the effective template name is unchanged.
INSERT IGNORE INTO go_container_template_definition
  (id, spec_id, image_id, display_name,
   r_library_path, python_library_path, conda_library_path, created_at, updated_at)
SELECT
   id, id, image_id, NULL,
   r_library_path, python_library_path, conda_library_path, created_at, updated_at
  FROM go_container_template;

-- ---------------------------------------------------------------------------
-- PHASE 2 (optional, irreversible): drop the legacy table.
-- Run only after the new UI/API has been validated against the migrated data.
-- ---------------------------------------------------------------------------

-- DROP TABLE go_container_template;
