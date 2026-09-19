# Migration: `go_container_template` → `go_container_template_spec` + `go_container_template_definition`

## Why

`ContainerTemplate` used to be a single persisted table (`go_container_template`)
that mixed two concerns:

* **how to run** – command, cpu/memory, work_dir, port, app_type, env, mounts,
  scheduling_constraint, labels, change_uid;
* **what to run** – `image_id`, plus the image-coupled package paths
  (`r_library_path`, `python_library_path`, `conda_library_path`).

It is now split into two tables, and `ContainerTemplate` is a **non-persisted
read model** assembled by `types.NewContainerTemplate(spec, definition, image)`:

| old | new |
| --- | --- |
| `go_container_template` (all columns) | `go_container_template_spec` — "how to run" |
| `go_container_template.image_id` + package paths | `go_container_template_definition` — the spec × image binding row |

### The invariant that makes the split non-breaking

```
go_container_template_definition.id  ==  go_container_template.id
```

`ContainerTemplate.ID` is defined as `ContainerTemplateDefinition.ID`, so the
three existing reference columns keep pointing at the same value and **need no
rewrite**:

| referencing table.column | semantics |
| --- | --- |
| `pipeline_components.container_template_id` | script → template |
| `go_container_app_session.container_template_id` | app session → template |
| `go_container_instance.template_id` | container instance → template |

`spec_id` is a fresh, unique key inside `go_container_template_spec`. The
migration **reuses the old template id for it** because the two columns live in
different tables (per-table uniqueness), so no snowflake minting is needed in
SQL. The old ids are already `< 2^63`, so no Go `int64` overflow risk — which is
why `UUID_SHORT()` (unsigned, can exceed `int64`) is deliberately *not* used.

`display_name` is left `NULL` on purpose: the read model falls back to the spec
name when it is empty, so the effective template name is unchanged.

## Prerequisites

The two new tables must exist before running PHASE 1. They are created by
`AutoMigrate` in `internal/container/container.go`
(`&types.ContainerTemplateSpec{}`, `&types.ContainerTemplateDefinition{}`), so
**start the new binary once** (the tables are created empty, nothing else
happens) or create them manually.

Nothing in the new code reads or writes `go_container_template` any more, and
GORM never drops tables — so the old table stays behind as a rollback source.

## What to run

Pick the script matching your `database.driver`:

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`

Both statements are idempotent (`INSERT IGNORE` / `ON CONFLICT DO NOTHING` /
`INSERT OR IGNORE`): re-running skips rows whose primary key already exists, so
specs edited after the migration are not overwritten.

1. **PHASE 1 (mandatory)** – copy every `go_container_template` row into one
   spec row **and** one definition row.
2. **PHASE 2 (optional)** – drop the legacy `go_container_template` table.

Keep PHASE 2 un-run until you have validated the new UI/API against the migrated
data: it is the only irreversible step.

## Verify

Row counts must match, and no reference may be left dangling:

```sql
SELECT (SELECT COUNT(*) FROM go_container_template)            AS old_rows,
       (SELECT COUNT(*) FROM go_container_template_spec)       AS specs,
       (SELECT COUNT(*) FROM go_container_template_definition) AS definitions;

SELECT COUNT(*) FROM pipeline_components pc
  LEFT JOIN go_container_template_definition d ON d.id = pc.container_template_id
 WHERE pc.container_template_id > 0 AND d.id IS NULL;   -- expected 0

SELECT COUNT(*) FROM go_container_app_session s
  LEFT JOIN go_container_template_definition d ON d.id = s.container_template_id
 WHERE s.container_template_id > 0 AND d.id IS NULL;   -- expected 0

SELECT COUNT(*) FROM go_container_instance i
  LEFT JOIN go_container_template_definition d ON d.id = i.template_id
 WHERE i.template_id > 0 AND d.id IS NULL;             -- expected 0
```

`specs` and `definitions` equal `old_rows`, and the read model joins back
together:

```sql
SELECT d.id,
       COALESCE(NULLIF(d.display_name, ''), s.name) AS template_name,
       d.spec_id, d.image_id, i.full_name AS image, s.port, s.app_type
  FROM go_container_template_definition d
  JOIN go_container_template_spec s ON s.id = d.spec_id
  LEFT JOIN go_container_image     i ON i.id = d.image_id
 ORDER BY d.id;
```

This must reproduce the old `go_container_template` rows (name, image, port,
app_type, package paths).

## Rollback

Before PHASE 2 the rollback is a plain `DELETE`:

```sql
DELETE FROM go_container_template_definition;
DELETE FROM go_container_template_spec;
```

and redeploy the previous binary (which reads `go_container_template`). After
PHASE 2 the old table is gone — restore it from your backup first.
