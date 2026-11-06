# Drop the deprecated `go_container_template_spec.work_dir` column

`types.ContainerTemplateSpec` / `types.ContainerTemplate` no longer have a
`WorkDir` field, so a container template can no longer pin its own working
directory.

The runtime working directory is now decided at creation time in
`ContainerCreateWorker.executeCreateInstance`:

- default: the runtime variable `PROJECT_DIR` (`<storage.base_dir>/data/<projectID>`);
- DAG node (task) containers keep their node workspace: `applyTaskRuntimeSpec`
  still overrides `ContainerSpec.WorkDir` with `WORKSPACE_PATH`
  (`<storage.base_dir>/data/<projectID>/analysis_node/...`), because `run.sh`
  lives there.

Exported assets (`script.json` / `workflow.json` / builtin templates) no longer
carry `work_dir`; the JSON decoder ignores the field, so old files still import.

GORM `AutoMigrate` never drops columns, so remove it explicitly:

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`

Order: stop the old binary -> run the DDL -> start the new binary.

The column is nullable (`varchar(512)` with no `NOT NULL`), so binaries that
still write it and binaries that ignore it can coexist during a rolling
restart — the only visible effect is that the template form value stops being
used.
