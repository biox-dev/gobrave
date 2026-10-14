# Project identity: drop the string business id, use the `int64` PK everywhere

`types.Project.ProjectID` (a uuid `varchar` business id, `t_project.project_id`)
was **removed**. The project's primary key `t_project.id` (`bigint`) is now the
only project identifier, and it is also the storage directory key
(`data/<id>/…`).

Every table that referenced a project by the old `project_id` varchar must be
rewritten to hold the numeric PK instead:

| Table                | Column             | Old type       | New type      |
|----------------------|--------------------|----------------|---------------|
| `user_project`       | `project_id`       | `varchar(255)` | `bigint`      |
| `t_project_report`   | `project_id`       | `varchar(255)` | `bigint`      |
| `go_project_dataset` | `project_id`       | `varchar(191)` | `bigint`      |
| `project_literature` | `project_id`       | `varchar(255)` | `bigint`      |
| `t_literature`       | `owner_project_id` | `varchar(255)` | `bigint` NULL |

`t_project.project_id` is dropped at the end.

## Why this is a backfill, not a cast

The existing values are uuids (e.g. `7fc3625a-fc85-4b1e-b011-a5244e2b8334`),
**not** numeric strings, so they cannot be converted to `bigint` directly. Each
table is migrated by joining its old string value against
`t_project.project_id` to resolve the numeric `t_project.id`.

`AutoMigrate` never changes a column type nor copies data, so this must be run as
an explicit DDL migration.

## Order / caveats

- **Order:** stop the old binary → run the DDL → start the new binary. Old
  binaries read the uuid `project_id`, so do not roll back the binary after
  running this migration without restoring a backup first.
- **Back up first.** The script renames/drops columns and is **not**
  re-runnable. MySQL DDL auto-commits, so the statements are ordered so that a
  mid-way failure leaves the temporary `*_new` columns intact while the old
  columns are dropped last.
- **Pre-check:** the script assumes every referencing string value resolves to a
  `t_project` row. Run the commented `SELECT COUNT(*) … WHERE p.id IS NULL`
  queries at the top of `mysql.sql` first and expect `0` for each; a non-zero
  count means a dangling reference that must be fixed (or nulled) before the
  `NOT NULL` columns are rewritten.
- **On-disk data:** existing project directories are `data/<uuid>/…`. After this
  migration the application keys directories by the numeric PK, so move
  `data/<uuid>` → `data/<t_project.id>` for each project (the mapping is exactly
  the `t_project` join used below).

## Files

- `mysql.sql` (requested; the primary deliverable)
