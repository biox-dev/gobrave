# App session kind

`go_container_app_session` now records **which kind of session** a row is, so the
UI can tell a **web app session** apart from a **terminal session**.

New column:

- `session_kind` — `web` | `terminal`, not null, default `web`, indexed.
  - `web` — created by `ContainerService.CreateAppSessionByTemplate`: the
    container runs the template command and the browser reaches the app through
    the gateway.
  - `terminal` — created by `TerminalSessionRegistry.createOrAdopt`: the
    container runs the keep-alive command and the interactive shell is opened by
    `exec`.

`session_kind` describes the session itself, not who created it: the creator is
already recorded in `agent_session_id` (0 = UI, >0 = agent session) and the
lifetime policy in `persistent`. All three are written by Go code, so new rows
never rely on the DDL default.

GORM `AutoMigrate` adds the column, but it never backfills data:

```sql
UPDATE go_container_app_session
SET session_kind = 'terminal'
WHERE session_kind = 'web'
  AND (agent_session_id > 0 OR persistent = 1);
```

Terminal sessions always have `agent_session_id > 0` (agent-created) or
`persistent = 1` (created from the UI / opened in the browser), while web app
sessions have neither — so the statement is safe to re-run.

Order: stop the binary -> run the DDL -> start the new binary.

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`
