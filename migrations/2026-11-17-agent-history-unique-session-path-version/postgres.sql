-- agent_history_files 的唯一键必须是 (session_id, path, version)。
-- 详见同目录 mysql.sql 的说明：旧索引是 (path, version)，不同会话写同一路径的
-- 同一版本会触发唯一键冲突。

DROP INDEX IF EXISTS uk_history_session_path_version;

CREATE UNIQUE INDEX uk_history_session_path_version
  ON agent_history_files (session_id, path, version);
