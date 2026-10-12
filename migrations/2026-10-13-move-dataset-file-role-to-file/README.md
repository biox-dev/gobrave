# Move the dataset-file role onto `go_file`

`types.DatasetFile` no longer has a `Role` field. The role now lives on the file
itself (`go_file.role`), the same column analysis-node outputs already use, so a
file has a single role regardless of how many datasets it is attached to.

Every code path was updated accordingly:

- `AddFileToDataset` writes the requested role onto `go_file` (creating or
  updating the file) instead of the join row.
- The project file list / role filter joins `go_dataset_file` only for the
  binding and reads the role from `go_file.role` (`PageFileByProjectID`,
  `ListFileByProjectID`).
- The `/data/dataset-file/update` endpoint was removed: editing a role now goes
  through `/data/file/update` (the `role` field of `UpdateFileRequest`).

GORM `AutoMigrate` never drops columns and never copies data, so the old
`go_dataset_file.role` column would linger and its values would be ignored. Run
the DDL below once to backfill `go_file.role` from the existing bindings and drop
the obsolete column.

Order: stop the old binary -> run the DDL -> start the new binary. Older binaries
still read `go_dataset_file.role`, so do not roll back the binary after dropping
the column without re-adding it and restoring the data first.

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`
