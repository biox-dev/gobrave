-- Agent 消息的 assistant 文本统一为 kind = 'assistant'。
--
-- 旧版本把「Provider 没有完整 message 块时的兜底回答」和「子 Agent 最终回答」
-- 写成 kind = 'assistant_final'；新代码只写 'assistant'。
UPDATE agent_messages
SET kind = 'assistant'
WHERE kind = 'assistant_final';
