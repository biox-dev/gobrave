# Migration: rename `go_file.role` to `go_file.file_key`

## Why

The column that used to be named `go_file.role` never described a *role*: it is
the **key** a file carries inside its owning assay (`FASTQ_R1`, `BAM`, ...), and
analysis form inputs map it onto the key of their `resolver.accept_formats`
(see `buildParseAnalysisResult`).

The actual *role* now lives on the dataset binding, exactly where the form
resolver expects it:

| binding | role column |
| --- | --- |
| `go_dataset_file` | `role` (file's role inside the dataset) |
| `go_dataset_assay` | `role` (assay's role inside the dataset — this rename's sibling migration) |

So this is a pure rename of the same business value: `go_file.role` →
`go_file.file_key`. No semantics change, no data loss.

Go API (compiler-enforced, no compatibility shim):

- `types.File.Role` → `types.File.FileKey` with JSON `file_key`, column
  `file_key`
- `POST /data/file/create` / `POST /data/file/update` now take `file_key`
  instead of `role`

## Applying

`AutoMigrate` adds `go_file.file_key` but never removes `go_file.role`, so:

1. Start the new binary once (or add the column by hand) so `file_key` exists.
2. Run **PHASE 1** of `mysql.sql` / `postgres.sql` / `sqlite.sql` to copy the
   existing values onto `file_key`.
3. Run **PHASE 2** to drop the legacy `role` column. This is irreversible.

## Rollback

Re-add `role` and copy back:

```sql
ALTER TABLE go_file ADD COLUMN role varchar(64) NULL;
UPDATE go_file SET role = file_key;
```
