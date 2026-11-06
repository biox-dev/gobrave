-- App sessions record which kind of session they are (see types.AppSessionKind*).
--   'web'      : ContainerService.CreateAppSessionByTemplate (template command, served through the gateway)
--   'terminal' : TerminalSessionRegistry.createOrAdopt (keep-alive container + interactive PTY shell)
ALTER TABLE go_container_app_session
  ADD COLUMN session_kind VARCHAR(32) NOT NULL DEFAULT 'web';

CREATE INDEX idx_go_container_app_session_session_kind
  ON go_container_app_session (session_kind);

-- Backfill: terminal sessions always carry an agent session id or the
-- persistent flag; every other row was created by the web path.
UPDATE go_container_app_session
SET session_kind = 'terminal'
WHERE session_kind = 'web'
  AND (agent_session_id > 0 OR persistent = 1);
