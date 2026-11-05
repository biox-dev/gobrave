-- Terminal sessions become AppSessions.
-- 1) New columns on go_container_app_session (GORM AutoMigrate also adds them).
ALTER TABLE go_container_app_session ADD COLUMN agent_session_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE go_container_app_session ADD COLUMN persistent BOOLEAN NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_go_container_app_session_agent_session_id
  ON go_container_app_session (agent_session_id);

-- 2) Move every running terminal session into go_container_app_session.
--    go_terminal_session.id becomes the app session id, so the container owner
--    relation below stays 1:1.
INSERT INTO go_container_app_session (
  id, user_id, project_id, analysis_node_id, container_template_id,
  name, status, workspace_path, agent_session_id, persistent, last_access_at,
  created_at, updated_at
)
SELECT
  ts.id,
  ts.user_id,
  ts.project_id,
  0,
  ts.template_id,
  ts.name,
  'RUNNING',
  '',
  ts.agent_session_id,
  ts.persistent,
  ts.last_active_at,
  ts.created_at,
  ts.updated_at
FROM go_terminal_session ts
WHERE ts.status = 'running'
  AND NOT EXISTS (SELECT 1 FROM go_container_app_session s WHERE s.id = ts.id);

-- 3) Re-point the terminal containers at the migrated sessions so the registry
--    can adopt them (owner_type=app_session, owner_id=app_session.id).
UPDATE go_container_instance
SET owner_type = 'app_session',
    owner_id = (
      SELECT ts.id
      FROM go_terminal_session ts
      WHERE ts.id = go_container_instance.owner_id
    )
WHERE owner_type = 'terminal'
  AND EXISTS (
    SELECT 1 FROM go_terminal_session ts WHERE ts.id = go_container_instance.owner_id
  );

-- 4) Keep the old table for verification; drop it manually once the data above
--    has been checked.
ALTER TABLE go_terminal_session RENAME TO go_terminal_session_legacy;
