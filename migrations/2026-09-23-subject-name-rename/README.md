# Migration: rename `go_subject.subject_id` → `go_subject.subject_name`

## Why

`go_subject.subject_id` was never an id: it holds the subject's **business
name** (e.g. `Mouse-001`), while `go_subject.id` (int64, `utils.GenerateID()`)
is the real primary key. Worse, the name collided with
`go_sample.subject_id`, which is an int64 **foreign key** → `go_subject.id`:

| column | before | after | meaning |
| --- | --- | --- | --- |
| `go_subject.id` | `id` | `id` | int64 PK (unchanged) |
| `go_subject.subject_id` | `subject_id` | **`subject_name`** | varchar(255), **unique** business name (`Mouse-001`) |
| `go_sample.subject_id` | `subject_id` | `subject_id` | int64 FK → `go_subject.id` (**unchanged**) |
| `go_assay.sample_id` | `sample_id` | `sample_id` | int64 FK → `go_sample.id` (**unchanged**) |

After the rename the three names no longer mean two different things:
`Subject.ID` (PK) / `Subject.SubjectName` (business name) /
`Sample.SubjectID` (FK → `Subject.ID`).

## API / JSON changes that ship with this migration

| surface | before | after |
| --- | --- | --- |
| `Subject` (create/update/read) | `subject_id` | `subject_name` |
| `POST /data/subject/page` request | `subject_id` (filter) | `subject_name` (filter) |
| `SampleWithSubjectInfo` | `subject_code` | `subject_name` (plus the unchanged `subject_id` FK) |
| `AssayWithDatasetInfo` (`/data/assay/list-by-project-page`) | `subject_code` **and** `subject_id` (subject PK) | `subject_name` only |

The assay read model deliberately keeps just the display name (mirroring
`sample_name`); it no longer exposes the subject PK. The sample read model keeps
`subject_id` (the FK) because the sample form needs it.

## Applying — ORDER MATTERS

**Run this before starting the new binary.**

`AutoMigrate` cannot rename a column: given the new `types.Subject` it would
**add** an empty `subject_name` (with a unique index) and leave `subject_id`
behind. With more than one subject row that unique index even fails to build
(duplicate `''`), so the new binary refuses to start — but on a single-row
database it would instead come up with the name silently lost. Either way the
values must be carried over by the SQL below, which is why the script is meant
to run *first*:

1. stop the old binary,
2. run `mysql.sql` / `postgres.sql` / `sqlite.sql`,
3. start the new binary (its `AutoMigrate` then only verifies the schema).

Every statement is guarded by an `information_schema` check, so re-running or
running it after step 3 is a no-op. InnoDB does not roll back DDL — take a
backup first.

### Recovery: if the new binary already ran AutoMigrate

The guarded script then refuses to act (the new column exists), leaving the old
values stranded in `subject_id`. Phases 2–3 at the bottom of `mysql.sql` /
`postgres.sql` copy them across and drop the legacy column; the unguarded
statements are commented out on purpose.

## Rollback

Rename back and move the values the other way:

```sql
ALTER TABLE go_subject CHANGE COLUMN subject_name subject_id VARCHAR(255) NOT NULL;
ALTER TABLE go_subject RENAME INDEX idx_go_subject_subject_name TO idx_go_subject_subject_id;
```

and deploy the previous binary.
