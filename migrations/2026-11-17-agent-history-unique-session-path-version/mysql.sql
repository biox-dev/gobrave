-- agent_history_files 的唯一键必须是 (session_id, path, version)。
--
-- 早期版本的 types.File 只把 uniqueIndex 标在 path / version 上，AutoMigrate 建出的
-- 是 UNIQUE KEY uk_history_session_path_version (path, version)，之后模型补上
-- session_id 时 AutoMigrate 不会改写已存在的索引，线上库里就一直是旧定义。
-- 结果：不同会话记录同一路径的同一版本时报
--   Error 1062 (23000): Duplicate entry '<path>-initial' for key
--   'agent_history_files.uk_history_session_path_version'
-- （MySQL 报错文案把复合键值截断到 64 字符，所以看起来只有 path）。
--
-- 触发场景：fork 会话复制文件历史（每个 path 都以 initial 版本写入新会话）、
-- 两个会话先后编辑同一个文件。
--
-- 只需执行一次；重复执行会在 ADD UNIQUE KEY 时报 Duplicate key name。

ALTER TABLE agent_history_files
  DROP INDEX uk_history_session_path_version;

ALTER TABLE agent_history_files
  ADD UNIQUE KEY uk_history_session_path_version (session_id, path, version);
