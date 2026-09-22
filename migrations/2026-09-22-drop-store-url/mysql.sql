-- store.url is obsolete: remote urls live in the store git repository — MySQL
-- See README.md in this folder for context and the caveats.
-- InnoDB does not roll back DDL, so take a backup first.

-- PHASE 1 (optional, recommended): archive / inspect the urls before dropping them.
-- SELECT id, store_type, name, origin, path_name, url FROM store WHERE url IS NOT NULL AND url <> '';

-- PHASE 2 (irreversible): drop the legacy column.
-- MySQL has no DROP COLUMN IF EXISTS: if the column was already dropped this errors with
-- "Can't DROP 'url'", which is safe to ignore.
ALTER TABLE store DROP COLUMN url;
