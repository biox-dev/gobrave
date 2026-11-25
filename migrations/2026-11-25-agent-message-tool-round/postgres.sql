-- agent_messages 增加恢复执行所需的轮次 / 工具调用标识（说明见 mysql.sql）。

ALTER TABLE agent_messages
  ADD COLUMN IF NOT EXISTS turn_id VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE agent_messages
  ADD COLUMN IF NOT EXISTS tool_call_id VARCHAR(128) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_agent_messages_turn_id
  ON agent_messages (turn_id);

CREATE INDEX IF NOT EXISTS idx_agent_messages_tool_call_id
  ON agent_messages (tool_call_id);
