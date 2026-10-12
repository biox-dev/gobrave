-- Move the file role from the go_dataset_file join table to go_file.
-- 1) Backfill go_file.role from the binding for rows that have no role yet.
UPDATE go_file f
SET role = df.role
FROM go_dataset_file df
WHERE df.file_id = f.id
  AND f.role IS NULL
  AND df.role IS NOT NULL
  AND df.role <> '';

-- 2) Drop the now-unused column from the join table.
ALTER TABLE go_dataset_file DROP COLUMN IF EXISTS role;
