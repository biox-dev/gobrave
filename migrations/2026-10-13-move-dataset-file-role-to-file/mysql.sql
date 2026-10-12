-- Move the file role from the go_dataset_file join table to go_file.
-- 1) Backfill go_file.role from the binding for rows that have no role yet.
UPDATE go_file f
JOIN go_dataset_file df ON df.file_id = f.id
SET f.role = df.role
WHERE f.role IS NULL
  AND df.role IS NOT NULL
  AND df.role <> '';

-- 2) Drop the now-unused column from the join table.
ALTER TABLE go_dataset_file DROP COLUMN role;
