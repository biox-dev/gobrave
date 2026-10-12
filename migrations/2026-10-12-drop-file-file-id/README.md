# Drop the deprecated `go_file.file_id` business column

`types.File` no longer has a `FileID` field: a file is identified solely by its
primary key `go_file.id`. Every code path (creates, lookups, the
`/sheet/workbook/by-file-id` read/write endpoints) now uses that primary key.

GORM `AutoMigrate` never drops columns, and the existing column is declared
`NOT NULL` (with a unique index). Once the new binary runs, inserts no longer
write `file_id`, so on MySQL in strict mode every file insert fails with
`Field 'file_id' doesn't have a default value`. Run the DDL below to remove the
column.

Order: stop the old binary -> run the DDL -> start the new binary. Older
binaries still expect the column, so do not roll back the binary after dropping
it without re-adding the column first.

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`
