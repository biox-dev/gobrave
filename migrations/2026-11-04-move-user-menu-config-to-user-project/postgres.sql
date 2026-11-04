-- Move the menu configuration from users to the user's active project binding.
-- 1) Add the new column (GORM serializer:json => TEXT).
ALTER TABLE user_project ADD COLUMN IF NOT EXISTS menu_config TEXT;

-- 2) Backfill each user's active binding from the legacy users.menu_config.
UPDATE user_project up
SET menu_config = u.menu_config
FROM users u
WHERE u.id = up.user_id
  AND up.is_active = true
  AND u.menu_config IS NOT NULL
  AND u.menu_config <> '';

-- 3) Drop the now-unused column.
ALTER TABLE users DROP COLUMN IF EXISTS menu_config;
