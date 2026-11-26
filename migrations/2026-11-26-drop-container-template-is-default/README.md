# Drop the deprecated `go_container_template_definition.is_default` column

`types.ContainerTemplateDefinition` / `types.ContainerTemplate` no longer have
an `IsDefault` field: there is no longer a "default container template".

`create_script` now requires an explicit `container_template_id` and no longer
falls back to a system default; the builtin templates no longer mark a default
row either. The `GET /container/template/default` and
`POST /container/template/set-default` endpoints were removed as well.

Because the "default" concept was only used to resolve the implicit template for
`create_script`, removing it does not affect running containers, app sessions or
existing scripts — they all reference a concrete `container_template_id`.

GORM `AutoMigrate` never drops columns, so remove it explicitly:

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`

Order: stop the old binary -> run the DDL -> start the new binary.

The column is `NOT NULL DEFAULT false`, so binaries that still write it and
binaries that ignore it can coexist during a rolling restart — the only visible
effect is that the "set as default" flag stops being used.
