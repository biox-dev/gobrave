# User identity: `string` (uuid) → `int64` (data-preserving)

`types.User.ID` (and every `user_id` foreign key that references it) changed from
a `varchar(36)` uuid to a `bigint` id. `types.User` generates its primary key in
`BeforeCreate` via `utils.GenerateID()` (snowflake), same pattern as
`Script`/`Workflow`/`Project`, and the id is exposed to the frontend as a JSON
string (`json:"id,string"`).

## Why a mapping table is required

Existing rows hold uuids (e.g. `74a7c2e4-…`), which **cannot** be `CAST` to
`bigint`. The migration therefore:

1. builds a temporary mapping table `_user_id_map(old_id, new_id)`, where the new
   ids are `ROW_NUMBER()` values (`1,2,3,…`), ordered by `users.created_at`
   (real users first, then dangling references), then `old_id`;
2. for every `user_id` column: adds a temp `*_new bigint` column, backfills it by
   `JOIN`ing the mapping table, drops the old column and renames the new one.

**No rows are deleted** — users, project links, sessions, agent profiles,
conversations and memories are all preserved with remapped ids.

New ids are small sequential integers and never collide with application
snowflake ids (19-digit values).

## Affected tables / columns

| Table                       | Column                 | Old type       | New type   |
|-----------------------------|------------------------|----------------|------------|
| `users`                     | `id`                   | `varchar(36)`  | `bigint`   |
| `auth_tokens`               | `user_id`              | `varchar(36)`  | `bigint`   |
| `user_project`              | `user_id`              | `varchar(36)`  | `bigint`   |
| `go_container_app_session`  | `user_id`              | `varchar(36)`  | `bigint`   |
| `agent_profiles`            | `user_id`              | `varchar(64)`  | `bigint`   |
| `agent_conversations`       | `user_id`              | `varchar(64)`  | `bigint`   |
| `agent_memories`            | `user_id`              | `varchar(64)`  | `bigint`   |
| `agent_permission_requests` | `resolved_by`          | `varchar(64)`  | `bigint`   |
| `agent_question_requests`   | `resolved_by`          | `varchar(64)`  | `bigint`   |

The `agent_*` tables are assumed to exist (the operator runs this migration
after they have been created), so they are migrated exactly like the core
tables above — plain, unconditional `ALTER`s, no `information_schema` guards.

## Access tokens are invalidated

The signed JWT payload embeds the **old** uuid and its signature cannot be
recomputed in SQL. All `auth_tokens` rows are kept (with the remapped `user_id`)
but are set to `is_revoked = 1`; **users must log in again** after the migration.

## Order / caveats

- **Order:** stop the old binary → run this DDL → start the new binary.
  AutoMigrate never changes column types, so the DDL must run explicitly.
- **Back up first.** The script rewrites the `users` primary key and every
  `user_id`; MySQL DDL auto-commits, so there is no single transaction. The
  statements are ordered so that a mid-way failure can be completed by re-running
  (the mapping table is dropped and re-created at the start).
- **Dangling references:** child rows whose `user_id` no longer matches a `users`
  row still get a mapping entry (ordered after real users), so a `NOT NULL`
  backfill never yields `NULL`. Such rows keep pointing at a user id that does
  not exist — clean them up separately if needed.
- **UNION pitfall (important):** the mapping query selects only the id column
  (`UNION` on `old_id` alone). Do not add `created_at` into the `UNION` branches —
  `(old_id, created_at=NULL)` would not dedupe against
  `(old_id, created_at=<timestamp>)` and the mapping insert would fail with a
  duplicate primary key.
- `utils.InitSnowflake()` must have been called before the new binary serves
  traffic (it already is at startup).

## Files

- `mysql.sql` (the primary deliverable)
- `README.md`

