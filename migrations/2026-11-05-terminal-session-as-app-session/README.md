# Terminal sessions are AppSessions

`types.TerminalSession` (table `go_terminal_session`) is gone. A terminal
session is now an ordinary application session: `types.AppSession`
(`go_container_app_session`) plus its container (`ContainerInstance` with
`owner_type=app_session`, `owner_id=app_session.id`).

New columns on `go_container_app_session`:

- `agent_session_id` — 0 = created from the UI, >0 = created by an agent
  session (`agent_sessions.id`). A single agent session reuses one terminal per
  container template.
- `persistent` — true means the session is only reclaimed by the idle TTL or an
  explicit `DELETE /container/terminal/session/{id}`; false means a one-shot
  session that is reclaimed as soon as the last subscriber leaves.

Behaviour changes:

- `GET /container/terminal/session/list` is removed; use
  `POST /container/app-session/list-by-page` (the terminal session *is* an app
  session).
- `POST /container/terminal/session/create`, the WebSocket endpoint, exec,
  status and close all address the session by `AppSession.ID`.
- Terminal containers are ordinary app-session containers started with the
  keep-alive command, created through
  `ContainerManager.CreateByTemplateWithOptions(Sync: true)`.

GORM `AutoMigrate` adds the two new columns and their index, but it never copies
data, so run the DDL below once to move existing terminal sessions into
`go_container_app_session`. The old table is kept (renamed) so its data can be
verified before dropping it manually.

Order: stop the old binary -> run the DDL -> start the new binary.

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`
