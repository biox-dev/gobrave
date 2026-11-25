-- agent_messages 增加恢复执行所需的轮次 / 工具调用标识。
--
-- 任务中断后不再有独立的检查点（原 agent_tasks.resume_state）：恢复所需的事实
-- 全部来自 agent_messages，Agent 循环按 turn_id 把 assistant 文本 / tool_call /
-- tool_result 归为一轮，用 tool_call_id 配对调用与结果，算出「还有哪些工具没执行」。
--
-- 只新增可空默认空串的列与索引，不改写历史数据：
--   - 历史消息的 turn_id / tool_call_id 为空，不会被误判为「中断轮次」；
--   - 需要恢复的任务由新版本重新执行时自然写入这两个字段。
--
-- 可重复执行：ADD COLUMN / CREATE INDEX 在 MySQL 8 不支持 IF NOT EXISTS，
-- 重复执行会报 Duplicate column name，按报错确认列已存在即可。

ALTER TABLE agent_messages
  ADD COLUMN turn_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '产生该消息的模型响应轮次';

ALTER TABLE agent_messages
  ADD COLUMN tool_call_id VARCHAR(128) NOT NULL DEFAULT '' COMMENT '工具调用 / 结果配对的调用 ID';

ALTER TABLE agent_messages
  ADD INDEX idx_agent_messages_turn_id (turn_id);

ALTER TABLE agent_messages
  ADD INDEX idx_agent_messages_tool_call_id (tool_call_id);
