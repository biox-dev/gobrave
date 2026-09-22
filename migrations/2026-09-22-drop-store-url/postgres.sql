-- store.url is obsolete: remote urls live in the store git repository — PostgreSQL
-- See README.md in this folder for context and the caveats.
-- AutoMigrate does not drop columns, so the new binary keeps this column (unused) until you run it.

-- PHASE 1 (optional, recommended): archive / inspect the urls before dropping them.
-- SELECT id, store_type, name, origin, path_name, url FROM store WHERE url IS NOT NULL AND url <> '';

-- PHASE 2 (irreversible): drop the legacy column.
ALTER TABLE store DROP COLUMN IF EXISTS url;
