# Move the menu configuration from `users` to `user_project`

`types.User` no longer has a `MenuConfig` field. The menu configuration (main
area tabs + bottom panel with terminals / logs) is project-scoped, so it now
lives on the user↔project binding (`types.UserProject.MenuConfig`,
`user_project.menu_config`).

Behaviour changes:

- `GET /auth/me` still returns `user.menu_config`; the handler now fills it from
  the user's **active** `user_project` row (`ProjectService.GetActiveUserMenuConfig`).
- `POST /auth/menu-config` and `POST /auth/bottom-panel` keep the same request
  bodies, but write to `user_project.menu_config` of the active project
  (`ProjectService.UpdateActiveUserMenuConfig`). No active project → `404`.
- Switching projects therefore switches the whole tab bar / bottom panel; the UI
  re-fetches `/auth/me` after `/project/activate-project`.

GORM `AutoMigrate` adds the new column but never drops columns nor copies data,
so run the DDL below once to backfill the current configuration into each user's
active project and drop the obsolete column.

Order: stop the old binary -> run the DDL -> start the new binary. Older binaries
still read `users.menu_config`, so do not roll back the binary after dropping the
column without re-adding it and restoring the data first.

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`
