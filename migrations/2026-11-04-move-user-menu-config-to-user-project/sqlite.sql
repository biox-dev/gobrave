-- Move the menu configuration from users to the user's active project binding.
-- 1) Add the new column (GORM serializer:json => TEXT).
ALTER TABLE user_project ADD COLUMN menu_config TEXT;

-- 2) Backfill each user's active binding from the legacy users.menu_config.
UPDATE user_project
SET menu_config = (
  SELECT u.menu_config
  FROM users u
  WHERE u.id = user_project.user_id
)
WHERE is_active = 1
  AND EXISTS (
    SELECT 1
    FROM users u
    WHERE u.id = user_project.user_id
      AND u.menu_config IS NOT NULL
      AND u.menu_config <> ''
  );

-- 3) Drop the now-unused column (SQLite >= 3.35).
ALTER TABLE users DROP COLUMN menu_config;
