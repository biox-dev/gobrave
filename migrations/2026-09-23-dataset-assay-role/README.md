# Migration: add `role` to `go_dataset_assay`

## Why

An assay bound into a dataset now carries a **role** inside that binding, exactly
like a file does (`go_dataset_file.role`). Analysis form inputs declared with
`input_type=assay` match their `resolver.accept_formats` against this role
(`buildScriptFormData` / `buildWorkflowFormData`), and the resolved assays are
returned keyed by role just like the file case:

| binding | role column | matched against |
| --- | --- | --- |
| `go_dataset_file` | `role` | `resolver.accept_formats` of `input_type=file` items |
| `go_dataset_assay` | `role` | `resolver.accept_formats` of `input_type=assay` items |

Go API:
- `types.DatasetAssay.Role` (JSON `role`)
- `types.AssayWithDatasetInfo.Role` is filled from `go_dataset_assay.role` by the
  assay list/page read model
- `POST /data/dataset-assay/update` now writes `role`

## Applying

`AutoMigrate` adds `go_dataset_assay.role`; the DDL below is only needed when the
new binary has not been started yet.

No backfill is possible: the role is a new concept for the dataset↔assay
binding, so existing rows keep `NULL` until a caller sets one (`NULL`/empty means
"no role", so the binding is simply not returned to any assay form input).

## Rollback

```sql
ALTER TABLE go_dataset_assay DROP COLUMN role;
```
