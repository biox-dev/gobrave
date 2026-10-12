-- Move the file role from the go_dataset_file join table to go_file.
-- 1) Backfill go_file.role from the binding for rows that have no role yet.
UPDATE go_file
SET role = (
  SELECT df.role
  FROM go_dataset_file df
  WHERE df.file_id = go_file.id
    AND df.role IS NOT NULL
    AND df.role <> ''
  LIMIT 1
)
WHERE (role IS NULL OR role = '')
  AND EXISTS (
    SELECT 1
    FROM go_dataset_file df
    WHERE df.file_id = go_file.id
      AND df.role IS NOT NULL
      AND df.role <> ''
  );

-- 2) Drop the now-unused column from the join table (SQLite >= 3.35).
ALTER TABLE go_dataset_file DROP COLUMN role;
