-- Container instances record how they run (see types.ContainerInstanceKind*):
--   'web'      : web app container (template command, exposed through the gateway)
--   'terminal' : terminal session container (keep-alive + exec'd shell)
--   'dag_node' : DAG node task container (bash ./run.sh, exits when done)
ALTER TABLE go_container_instance
  ADD COLUMN kind VARCHAR(32) NOT NULL DEFAULT 'web';

CREATE INDEX idx_go_container_instance_kind
  ON go_container_instance (kind);

-- Backfill: dag node owners run the task script (bash ./run.sh).
-- Terminal containers share owner_type=app_session with web containers, so the
-- historical rows keep the default 'web'; new rows are written explicitly.
UPDATE go_container_instance
SET kind = 'dag_node'
WHERE owner_type = 'dag_node';
