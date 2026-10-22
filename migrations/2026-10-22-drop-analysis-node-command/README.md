# Drop the deprecated `analysis_nodes.command` column

`types.AnalysisNode` no longer has a `Command` field. A command node's run script
is the file at `analysis_nodes.command_path` (`<workspace_dir>/run.sh`); it is
written directly by the create / update entrypoints (`create_agent_analysis`,
`update_agent_analysis`, `POST /analysis/{id}/command`) via
`prepare.WriteCommandRunScript`.

`NodeRuntimePreparer.prepareCommandNode` now only creates the workspace / output
/ cache directories and writes `params.json` + `env.sh`; it no longer rewrites
`run.sh`, so the DB copy of the command is neither read nor written anymore.

GORM `AutoMigrate` never drops columns, so remove it explicitly:

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`

Order: stop the old binary -> run the DDL -> start the new binary. Nodes created
by the old binary already have `run.sh` on disk (it was written during creation),
so dropping the column does not lose any runnable command.
