-- store.url is obsolete: remote urls live in the store git repository — SQLite
-- See README.md in this folder for context and the caveats.
-- SQLite supports DROP COLUMN since 3.35.

-- PHASE 1 (optional, recommended): archive / inspect the urls before dropping them.
-- SELECT id, store_type, name, origin, path_name, url FROM store WHERE url IS NOT NULL AND url <> '';

-- PHASE 2 (irreversible): drop the legacy column.
ALTER TABLE store DROP COLUMN url;
