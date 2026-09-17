# Migration: storage paths become relative to `storage.base_dir`

## Why

The database used to persist **absolute** filesystem paths (`store.path`,
`nextflow.work_dir` / `output_dir` / `params_path` / …, `analysis_nodes.output_dir` /
`cache_dir` / `log_path` / …). Changing `storage.base_dir` therefore required
rewriting rows.

After this change only the **root directory** of each entity is persisted, and it
is stored **relative to `storage.base_dir`**:

| table | persisted path column | derived at read time |
| --- | --- | --- |
| `store` | *none* (`path_name` is the relative identifier) | `store/<path_name>` |
| `nextflow` | `workspace_dir` | `params.json`, `run.sh`, `run.log`, `trace.log`, `workflow.log`, `.nextflow.log` |
| `analysis_nodes` | `workspace_dir` | `output/`, `cached/`, `params.json`, `run.sh`, `command.log` |

Resolution happens in `internal/application/repository/analysis.go`
(`resolveAnalysis` / `resolveNode`) via `utils.ResolvePath`, and writes are
converted with `utils.RelPath`. `utils.ResolvePath` returns **absolute paths
as-is**, so a database that has not been migrated yet keeps working (only the
"move base_dir" benefit is missing).

## What to run

Pick the script matching your `database.driver`:

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`

Each script is idempotent-ish and split into two phases:

1. **backfill** – rewrite the remaining absolute roots into `data/...` form;
2. **cleanup** – drop the columns that are no longer mapped by GORM.

PHASE 1 IS MANDATORY before switching `base_dir`. PHASE 2 is optional: leaving
the legacy columns in place is harmless (GORM no longer selects or writes them)
and is the safer choice if you want a fast rollback.

> Note: `store.path` is **not** backfilled. `store.path_name` already holds the
> relative identifier for every row (published entries use the workflow/script
> ID, downloaded entries use `<owner>/<repo>`).

> If the legacy columns were created as `NOT NULL` without a default (possible if
> the schema originally came from the Python `brave` service), MySQL will reject
> the new INSERTs with `Field 'xxx' doesn't have a default value`. In that case
> run PHASE 2, or just make them nullable:
> `ALTER TABLE nextflow MODIFY output_dir varchar(255) NULL;` (repeat per column).
