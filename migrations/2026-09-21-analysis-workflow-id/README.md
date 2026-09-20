# Migration: `nextflow.relation_id` → `nextflow.workflow_id` (workflow PK)

## Why

`types.Analysis.WorkflowID` used to be a **string** that carried the workflow's
`relation_id` (a UUID from the Python era), stored in the column
`nextflow.relation_id`. The rest of the codebase has already moved to
**int64 primary keys** for workflows
(`WorkflowService.GetWorkflowByID`, `GetFormJSONByWorkflowID`,
`GetWorkflowVisByWorkflowID`, …), so every consumer had to parse the string back
into an int64 — and all the `strconv.ParseInt` / `parsePositiveInt64` wrappers
existed only to bridge that gap.

After this change:

| | before | after |
| --- | --- | --- |
| Go type | `WorkflowID string` | `WorkflowID int64` |
| DB column | `relation_id` (`varchar(255)`, UUID) | `workflow_id` (`bigint`, PK) |
| `AnalysisQuey.WorkflowID` | `string` (filter, must not be empty) | `int64` (filter, must be `> 0`) |
| `AnalysisRepository.ListAnalysisByWorkflowID` | `workflowID string` | `workflowID int64` |

`Workflow.WorkflowID` (Go, `types.Workflow`) is **not** touched: it is still the
`pipeline_components_relation.relation_id` UUID and remains the on-disk
directory / store `PathName` key for a published workflow.

## The mapping is NOT `relation_id` alone

`pipeline_components_relation.relation_id` is **not unique**: the same UUID is
reused by one row per project that installed the workflow (verified on the live
DB: 3 rows shared one UUID). Joining on `relation_id` alone fans out and assigns
a random project's workflow PK.

The analysis rows carry the project they belong to (`nextflow.project_id`), so
the correct mapping is the pair:

```
nextflow (project_id, relation_id)  ->  pipeline_components_relation.id
```

Verified on the live `gobrave` DB: all 72 `nextflow` rows resolve to **exactly
one** workflow PK with this pair (0 unmatched, 0 ambiguous).

## What to run

Pick the script matching your `database.driver`:

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`

Each script has two phases:

1. **PHASE 1 (mandatory)** – add `workflow_id`, backfill it from
   `pipeline_components_relation` by `(project_id, relation_id)`, create the
   `idx_nextflow_workflow_id` index.
2. **PHASE 2 (optional, destructive)** – drop the legacy `relation_id` column.

> **Prerequisite / idempotency note:** `AutoMigrate` (which runs on startup of
> the new binary) already adds `workflow_id` and its index, because the field is
> tagged `index:idx_nextflow_workflow_id`. If you started the new binary first,
> skip the `ADD COLUMN` / `CREATE INDEX` statements in PHASE 1 and run only the
> `UPDATE` backfill — otherwise the DDL statement fails with
> *duplicate column name*. AutoMigrate never drops columns, so `relation_id`
> stays behind as the rollback source until you run PHASE 2.

### Rows that cannot be mapped

An unmappable row (its `(project_id, relation_id)` no longer exists in
`pipeline_components_relation`, e.g. a deleted workflow) keeps `workflow_id =
NULL` and therefore reads back as `0`. Such analyses can no longer be run or
recovered (the DAG recovery path requires `workflow_id > 0`) but they stay
queryable and deletable. Check before PHASE 2:

```sql
SELECT id, project_id, relation_id FROM nextflow WHERE workflow_id IS NULL;
```
