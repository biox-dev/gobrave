-- Move the menu configuration from users to the user's active project binding.
-- 1) Add the new column (GORM serializer:json => LONGTEXT).
ALTER TABLE user_project ADD COLUMN menu_config LONGTEXT NULL;

-- 2) Backfill each user's active binding from the legacy users.menu_config.
UPDATE user_project up
JOIN users u ON u.id = up.user_id
SET up.menu_config = u.menu_config
WHERE up.is_active = 1
  AND u.menu_config IS NOT NULL
  AND u.menu_config <> '';

-- 3) Drop the now-unused column.
ALTER TABLE users DROP COLUMN menu_config;
