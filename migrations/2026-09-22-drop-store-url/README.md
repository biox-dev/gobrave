# Migration: drop `store.url` (remote urls now live in the store git repository)

## Why

`types.Store.URL` used to hold the remote repository address of a store. It was written in two
different places with two different meanings:

| writer | meaning | value |
| --- | --- | --- |
| `DownloadStore` | upstream clone source | the cloned `<owner>/<repo>` url |
| `PublishStoreRemote` | publish target (github / gitee) | the url the user typed |

Keeping the address in a DB column duplicated state that already exists in git and drifted easily:
a store is a **bare git repository** under `storage.base_dir/store/<path_name>`, so

- after `DownloadStore`, `origin` already points at the clone url;
- after `PublishStoreRemote`, every publish target is a remote on that same repository.

After this change the git repository is the **single source of truth**:

- `PublishStoreRemote` adds/keeps a remote (`github`, `gitee`, …) on the store bare repo instead of
  writing a DB column — the same url published twice is detected via the existing remote and is
  **not** added again (it goes straight to the push step, so re-publishing an unchanged component just
  reports "already up to date");
- `GitState.remotes` (read side, `utils.ReadGitRemotes`) reports all configured remotes for
  `GetScriptById` / `GetWorkflowById`;
- `DownloadStore` de-duplicates by comparing the requested url against each store repository's
  remotes (`findStoreByRemoteURL`), which is equivalent to the old `GetStoreByURL` lookup.

Removed Go API (compiler-enforced, no data migration needed for them):

- `types.Store.URL`
- `interfaces.StoreService.GetStoreByURL` / `UpdateStoreURL`
- `interfaces.StoreRepository.GetStoreByURL` / `UpdateStoreURL`
- `exportcodec.ScriptInstallRequest.StoreURL` / `WorkflowInstallRequest.StoreURL` (were no-ops)

## What to run

Pick the script matching your `database.driver`:

- `mysql.sql`
- `postgres.sql`
- `sqlite.sql`

`AutoMigrate` **does not** drop columns, so the new binary leaves `store.url` in place (unused,
nullable). Dropping it is optional and irreversible.

## Before dropping (recommended)

Backfill the values into the git repositories so nothing is lost. For rows whose url looks like a
clone source (`origin = 'remote'`), the clone already recorded it as `origin`; for locally published
stores, re-run *Publish remote* once from the UI (or keep the column as an archive).

```sql
-- list the urls that only exist in the DB
SELECT id, store_type, name, origin, path_name, url
  FROM store
 WHERE url IS NOT NULL AND url <> '';
```
