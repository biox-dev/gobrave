# Migration: drop `go_assay_file`, bind files to assays directly

## Why

A file is produced by exactly one assay, so `Assay -> File` is a plain 1:N
relation. It used to be modelled with a join table:

```
go_assay_file(id, assay_id, file_id, role, lane, replicate)
```

That table only added indirection: `go_file` is already created once per physical
path and nothing else referenced the binding, so the join table could not express
anything the file row could not. The binding now lives on the file:

| column | notes |
| --- | --- |
| `go_file.assay_id` | int64 FK → `go_assay.id`, indexed. `NULL`/`0` = the file is **not** owned by an assay (dataset-only attachment) |
| `go_file.role` | varchar(64), the file's role inside its assay (e.g. `FASTQ`, `BAM`); analysis form inputs match this against their accept formats |

`lane` and `replicate` are **dropped**: nothing read them — they were only stored
and echoed back by the removed `/data/assay-file/*` CRUD endpoints. Add them back
on `go_file` if a real consumer appears.

Provenance chain after this migration:

```
Project → Subject → Sample → Assay → File
```

## API changes

Removed (nothing in the UI ever called them — `go-brave-ui` only had unused
wrappers):

- `POST /data/assay-file/create`
- `POST /data/assay-file/update`
- `POST /data/assay-file/delete`
- `GET  /data/assay-file/get`
- `GET  /data/assay-file/list`
- `GET  /data/assay-file/list-by-assay`

Added:

- `GET /data/file/list-by-assay?assay_id=<pk>` — files owned by one assay (empty
  array when the assay has none)

Changed:

- `POST /data/file/create` and `POST /data/file/update` accept `assay_id` and
  `role`. A non-zero `assay_id` must reference an existing assay, otherwise the
  API answers `404`. Both follow the existing "empty means keep" convention of
  `UpdateFile`, so they can be set but not cleared through the API.
- `AddFileToDataset` de-duplicates by `(path, assay_id = 0)`: a path registered
  for a dataset is a **different row** from the same path owned by an assay.

Removed Go API (compiler-enforced):

- `types.AssayFile`
- `interfaces.DataService` / `interfaces.DataRepository` AssayFile methods
- `interfaces.DataRepository.GetFileByPath` → replaced by `GetFileByPathAndAssayID`

## Behaviour changes to be aware of

- **Deleting an assay now deletes the files it owns** (and their dataset
  bindings) through `DeleteAssayWithRelations`. Previously only the join rows
  disappeared and the files survived. This follows from files being
  assay-private: an owned file cannot outlive its assay.
- `DeleteFileWithRelations` no longer touches `go_assay_file` (there is nothing
  left to clean up); the assay binding disappears with the file row.

## Applying

Order matters because the backfill needs the new columns:

1. Start the new binary once (or add the columns by hand). `AutoMigrate` adds
   `go_file.assay_id`, `go_file.role` and the `idx_go_file_assay_id` index; it
   never drops tables or columns.
2. Run **PHASE 1** of `mysql.sql` / `postgres.sql` / `sqlite.sql` to copy every
   existing `go_assay_file` row onto its file (a no-op when the table is empty).
3. Run **PHASE 2** to drop the legacy `go_assay_file` table. This is
   irreversible; the backfill in step 2 is what makes it safe.

Rollback: re-create the table with the DDL at the bottom of `mysql.sql`,
re-insert one row per `(assay_id, file_id)` pair from `go_file`, then null
`go_file.assay_id` / `go_file.role`.

## Legacy table DDL (for reference / rollback)

```sql
CREATE TABLE IF NOT EXISTS go_assay_file (
  id        bigint       NOT NULL,
  assay_id  bigint       NOT NULL,
  file_id   bigint       NOT NULL,
  role      varchar(64)  DEFAULT NULL,
  lane      varchar(32)  DEFAULT NULL,
  replicate varchar(32)  DEFAULT NULL,
  created_at datetime(3) DEFAULT NULL,
  PRIMARY KEY (id),
  KEY idx_go_assay_file_assay_id (assay_id),
  KEY idx_go_assay_file_file_id (file_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```
