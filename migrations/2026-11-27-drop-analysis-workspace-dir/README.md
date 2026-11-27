# Drop `nextflow.workspace_dir` / `analysis_nodes.workspace_dir`

`types.AnalysisWorkflow` and `types.AnalysisNode` no longer persist a directory.
Every path is now derived from the storage root plus the row's own keys, the same
way `Script.Dir(baseDir)` / `Workflow.Dir(baseDir)` work:

```
AnalysisWorkflow.Dir() = <base>/data/<project_id>/analysis/<workflow_id>/<analysis_id>
AnalysisNode.Dir()     = <base>/data/<project_id>/analysis_node/<script_id>/<node_id>
```

`base` is `storage.base_dir`; it is injected by the repository at read time and
is never a database column.

Consequences worth noting before running the DDL:

- `analysis_nodes` now has **one** layout for all three node kinds (script node,
  agent command node, scheduler node): `analysis_node/<script_id>/<node_id>`,
  where a command node has no script and therefore uses `0` as its segment.
  Scheduler nodes no longer live below the analysis directory.
- Existing on-disk node directories are **not** moved by this migration. Rows
  written by an older binary will be looked up under the new path, so either
  re-run those analyses or move the directories beforehand:

      # old script node:    data/<p>/analysis_node/<script_id>/<node_id>
      # old scheduler node: data/<p>/analysis/<workflow_id>/<analysis_id>/<node_id>
      # new layout:         data/<p>/analysis_node/<script_id>/<node_id>
      #                     (scheduler nodes use their bound script_id;
      #                      command nodes use 0)

GORM `AutoMigrate` never drops columns, so remove them explicitly:

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`

Order: stop the old binary -> run the DDL -> start the new binary.
