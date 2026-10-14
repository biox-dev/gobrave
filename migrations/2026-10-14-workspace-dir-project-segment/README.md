# Rewrite the project segment inside `workspace_dir`

Companion to `2026-10-14-project-pk-as-project-id` (which flipped the project FK
columns to the numeric PK and dropped `t_project.project_id`).

That migration changed the **keys** the code uses, but the persisted
`workspace_dir` strings still embed the **old project segment**:

```
data/<old-key>/analysis/<workflowID>/<analysisID>
data/<old-key>/analysis_node/<scriptID>/<nodeID>
/home/admin/.brave/analysis/<old-key>/<...>/node-<uuid>   (legacy, absolute)
```

where `<old-key>` is the removed `t_project.project_id` — a uuid
(`38db8d90-81f6-4fb0-be5f-202c9584cb2e`) or the literal `default`.

After the refactor the directory is derived from the numeric PK
(`utils.GetProjectDir` → `data/<int64>`), so every persisted `workspace_dir`
must be rewritten from `/<old-key>/` to `/<numeric-PK>/`.

## Why it is a data migration, not a JOIN

`t_project.project_id` is **already dropped** by the time this runs, so the
uuid→id mapping cannot be looked up. It is recovered from the data itself:

```sql
SELECT project_id, SUBSTRING_INDEX(SUBSTRING_INDEX(workspace_dir,'/',2),'/',-1) seg
FROM nextflow WHERE workspace_dir LIKE 'data/%'
GROUP BY project_id, seg;
```

Verified on the live DB — each old segment maps to **exactly one** numeric PK
(no segment is shared by two projects), so a `REPLACE` cannot rewrite another
project's rows:

| old segment                            | numeric PK              | nextflow rows | node rows |
|----------------------------------------|-------------------------|---------------|-----------|
| `default`                              | `1`                     | 7             | 170       |
| `d9a2f545-09d9-4066-a800-2b07d231cf56` | `2`                     | 1             | 5         |
| `38db8d90-81f6-4fb0-be5f-202c9584cb2e` | `2098666223674855424`   | 76            | 220       |

## How it works

`REPLACE(workspace_dir, '/<old-key>/', '/<pk>/')` with **leading and trailing
slashes** so only a whole path component is touched — never a partial substring
elsewhere in the path. Both relative (`data/<key>/…`) and legacy absolute
(`/…/analysis/<key>/…`) layouts contain `/<key>/`, so one `REPLACE` per segment
covers both.

The statements are **idempotent**: once `<old-key>` is replaced it no longer
appears, so re-running is a no-op.

## Order / caveats

- **Order:** run **after** `2026-10-14-project-pk-as-project-id`. Back up first.
- **`project_id IS NULL` rows:** 180 legacy `analysis_nodes` rows have a NULL
  `project_id` (old absolute layout) but still carry the `default` segment, so
  the `/default/` → `/1/` update also fixes them.
- The commented `SELECT … REGEXP` block at the end of `mysql.sql` verifies no
  uuid/`default` segment remains.
- **On-disk data:** the actual directories under `storage.base_dir` must be moved
  in lock-step: `data/<old-key>` → `data/<pk>` (same mapping used here).

## Files

- `mysql.sql`
