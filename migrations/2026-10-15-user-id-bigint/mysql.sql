-- Migration: 2026-10-15-user-id-bigint
-- User identity string (uuid) -> int64 for `users.id` and every user-scoped
-- `user_id` column, PRESERVING existing data.
--
-- STRATEGY: uuid values cannot be CAST to bigint, so a temporary mapping table
-- `_user_id_map(old_id, new_id)` is built first (new ids = ROW_NUMBER() 1..N,
-- ordered by users.created_at then users.id), then every `user_id` column is
-- backfilled through a JOIN on that map. No rows are deleted. See README.md.
--
-- New ids are small sequential integers and therefore never collide with
-- application-generated snowflake ids (19-digit values).
--
-- NOTE: existing access tokens are revoked — the JWT payload embeds the OLD
-- uuid and its signature cannot be recomputed in SQL. Users log in again.
--
-- !! BACK UP FIRST. This rewrites the `users` primary key and every `user_id`. !!
--
-- PRE-CHECK (run manually; every user-scoped table must appear here):
--   SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE
--   FROM information_schema.COLUMNS
--   WHERE TABLE_SCHEMA = DATABASE()
--     AND COLUMN_NAME IN ('user_id', 'resolved_by')
--   ORDER BY TABLE_NAME;

-- ============================================================================
-- 0) Mapping table: old uuid -> new sequential int64 (data-preserving).
-- ============================================================================
DROP TABLE IF EXISTS `_user_id_map`;
CREATE TABLE `_user_id_map` (
  `old_id` varchar(64) NOT NULL,
  `new_id` bigint NOT NULL,
  PRIMARY KEY (`old_id`),
  UNIQUE KEY `uk_user_id_map_new` (`new_id`)
) ENGINE=InnoDB;

INSERT INTO `_user_id_map` (`old_id`, `new_id`)
SELECT ids.`old_id`,
       ROW_NUMBER() OVER (ORDER BY (u.`id` IS NULL), u.`created_at`, ids.`old_id`)
FROM (
  -- DISTINCT on the id only: the same uuid appears in `users` AND in child
  -- tables; a plain UNION would treat (old_id, created_at=NULL) as distinct
  -- from (old_id, created_at=<ts>) and produce duplicate map rows.
  SELECT `id` AS `old_id` FROM `users`
  UNION
  SELECT `user_id` FROM `auth_tokens`
  UNION
  SELECT `user_id` FROM `user_project`
  UNION
  SELECT `user_id` FROM `go_container_app_session`
) AS ids
LEFT JOIN `users` u ON u.`id` = ids.`old_id`
WHERE ids.`old_id` IS NOT NULL;

-- ============================================================================
-- 1) users.id : varchar(36) PK -> bigint PK (data preserved).
--    Add temp column -> backfill via map -> drop old PK/column -> rename.
-- ============================================================================
ALTER TABLE `users` ADD COLUMN `id_new` bigint NULL AFTER `id`;

UPDATE `users` u
  JOIN `_user_id_map` m ON m.`old_id` = u.`id`
SET u.`id_new` = m.`new_id`;

ALTER TABLE `users`
  DROP PRIMARY KEY,
  DROP COLUMN `id`;
ALTER TABLE `users`
  CHANGE COLUMN `id_new` `id` bigint NOT NULL FIRST,
  ADD PRIMARY KEY (`id`);

-- ============================================================================
-- 2) auth_tokens.user_id : varchar(36) -> bigint (rows preserved, then revoked)
-- ============================================================================
ALTER TABLE `auth_tokens` DROP INDEX `idx_auth_tokens_user_id`;
ALTER TABLE `auth_tokens` ADD COLUMN `user_id_new` bigint NULL AFTER `user_id`;

UPDATE `auth_tokens` t
  JOIN `_user_id_map` m ON m.`old_id` = t.`user_id`
SET t.`user_id_new` = m.`new_id`;

ALTER TABLE `auth_tokens`
  DROP COLUMN `user_id`,
  CHANGE COLUMN `user_id_new` `user_id` bigint NOT NULL,
  ADD INDEX `idx_auth_tokens_user_id` (`user_id`);

-- Old tokens embed the old uuid in their signed JWT, so they can no longer be
-- validated against a numeric user id; revoke them all.
UPDATE `auth_tokens` SET `is_revoked` = 1;

-- ============================================================================
-- 3) user_project.user_id : varchar(36) -> bigint
--    idx_user_project_unique is (user_id, project_id); drop both user_id
--    indexes first, migrate the column, then re-create them.
-- ============================================================================
ALTER TABLE `user_project` DROP INDEX `idx_user_project_unique`;
ALTER TABLE `user_project` DROP INDEX `idx_user_project_user`;
ALTER TABLE `user_project` ADD COLUMN `user_id_new` bigint NULL AFTER `user_id`;

UPDATE `user_project` up
  JOIN `_user_id_map` m ON m.`old_id` = up.`user_id`
SET up.`user_id_new` = m.`new_id`;

ALTER TABLE `user_project`
  DROP COLUMN `user_id`,
  CHANGE COLUMN `user_id_new` `user_id` bigint NOT NULL;

ALTER TABLE `user_project`
  ADD INDEX `idx_user_project_user` (`user_id`),
  ADD UNIQUE INDEX `idx_user_project_unique` (`user_id`, `project_id`);

-- ============================================================================
-- 4) go_container_app_session.user_id : varchar(36) -> bigint
-- ============================================================================
ALTER TABLE `go_container_app_session` DROP INDEX `idx_go_container_app_session_user_id`;
ALTER TABLE `go_container_app_session` ADD COLUMN `user_id_new` bigint NULL AFTER `user_id`;

UPDATE `go_container_app_session` s
  JOIN `_user_id_map` m ON m.`old_id` = s.`user_id`
SET s.`user_id_new` = m.`new_id`;

ALTER TABLE `go_container_app_session`
  DROP COLUMN `user_id`,
  CHANGE COLUMN `user_id_new` `user_id` bigint NOT NULL,
  ADD INDEX `idx_go_container_app_session_user_id` (`user_id`);

-- ============================================================================
-- 5) agent_* tables (they exist, so migrated like the tables above).
--    Data is preserved via `_user_id_map` (resolved_by is a nullable column).
-- ============================================================================

-- agent_profiles.user_id : varchar(64) -> bigint
ALTER TABLE `agent_profiles` DROP INDEX `idx_agent_profiles_user`;
ALTER TABLE `agent_profiles` ADD COLUMN `user_id_new` bigint NULL AFTER `user_id`;

UPDATE `agent_profiles` p
  JOIN `_user_id_map` m ON m.`old_id` = p.`user_id`
SET p.`user_id_new` = m.`new_id`;

ALTER TABLE `agent_profiles`
  DROP COLUMN `user_id`,
  CHANGE COLUMN `user_id_new` `user_id` bigint NOT NULL,
  ADD INDEX `idx_agent_profiles_user` (`user_id`);

-- agent_conversations.user_id : varchar(64) -> bigint
ALTER TABLE `agent_conversations` DROP INDEX `idx_agent_conversations_user_id`;
ALTER TABLE `agent_conversations` ADD COLUMN `user_id_new` bigint NULL AFTER `user_id`;

UPDATE `agent_conversations` c
  JOIN `_user_id_map` m ON m.`old_id` = c.`user_id`
SET c.`user_id_new` = m.`new_id`;

ALTER TABLE `agent_conversations`
  DROP COLUMN `user_id`,
  CHANGE COLUMN `user_id_new` `user_id` bigint NOT NULL,
  ADD INDEX `idx_agent_conversations_user_id` (`user_id`);

-- agent_memories.user_id : varchar(64) -> bigint (composite index user_id, kind)
ALTER TABLE `agent_memories` DROP INDEX `idx_agent_memories_user_kind`;
ALTER TABLE `agent_memories` ADD COLUMN `user_id_new` bigint NULL AFTER `user_id`;

UPDATE `agent_memories` mem
  JOIN `_user_id_map` m ON m.`old_id` = mem.`user_id`
SET mem.`user_id_new` = m.`new_id`;

ALTER TABLE `agent_memories`
  DROP COLUMN `user_id`,
  CHANGE COLUMN `user_id_new` `user_id` bigint NOT NULL,
  ADD INDEX `idx_agent_memories_user_kind` (`user_id`, `kind`);

-- agent_permission_requests.resolved_by : varchar(64) -> bigint (nullable, no index)
ALTER TABLE `agent_permission_requests`
  ADD COLUMN `resolved_by_new` bigint NULL AFTER `resolved_by`;

UPDATE `agent_permission_requests` r
  JOIN `_user_id_map` m ON m.`old_id` = r.`resolved_by`
SET r.`resolved_by_new` = m.`new_id`;

ALTER TABLE `agent_permission_requests`
  DROP COLUMN `resolved_by`,
  CHANGE COLUMN `resolved_by_new` `resolved_by` bigint NULL;

-- agent_question_requests.resolved_by : varchar(64) -> bigint (nullable, no index)
ALTER TABLE `agent_question_requests`
  ADD COLUMN `resolved_by_new` bigint NULL AFTER `resolved_by`;

UPDATE `agent_question_requests` q
  JOIN `_user_id_map` m ON m.`old_id` = q.`resolved_by`
SET q.`resolved_by_new` = m.`new_id`;

ALTER TABLE `agent_question_requests`
  DROP COLUMN `resolved_by`,
  CHANGE COLUMN `resolved_by_new` `resolved_by` bigint NULL;

-- ============================================================================
-- 6) Cleanup
-- ============================================================================
DROP TABLE IF EXISTS `_user_id_map`;

-- ============================================================================
-- 7) Verify
-- ============================================================================
--   SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE
--   FROM information_schema.COLUMNS
--   WHERE TABLE_SCHEMA = DATABASE()
--     AND ((TABLE_NAME = 'users' AND COLUMN_NAME = 'id')
--       OR COLUMN_NAME IN ('user_id', 'resolved_by'))
--   ORDER BY TABLE_NAME;
--   -- Expect COLUMN_TYPE = 'bigint' for all of the above.
--
--   SELECT COUNT(*) FROM `users`;          -- unchanged row count
--   SELECT MIN(id), MAX(id) FROM `users`;  -- sequential 1..N
--   SELECT COUNT(*) FROM `auth_tokens` WHERE `is_revoked` = 0;  -- expect 0
