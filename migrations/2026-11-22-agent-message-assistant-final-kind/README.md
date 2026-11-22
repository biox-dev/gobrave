# Agent message assistant kind

把 `agent_messages.kind` 里的 assistant 文本统一为 `assistant`，删除历史遗留值
`assistant_final`。

## 背景

旧版本中 `assistant_final` 只用于两处：

- `TimelineRecorder.RecordResult`：Provider 只发增量、没有完整 `message` 块时，
  轮末用最终结果补写的兜底回答；
- 子 Agent 会话的最终回答（`subAgentSessionStore.AppendFinalMessage`）。

它和普通的 `assistant` 文本在语义上完全相同，前端需要额外兼容两种 kind，
实时流与历史消息去重时也要先做归一化。现在后端统一只写 `assistant`，
`KindAssistantFinal` 常量及相关代码已删除。

## 变更

无 DDL（`kind` 列与索引已存在），只有数据归一化：

```sql
UPDATE agent_messages
SET kind = 'assistant'
WHERE kind = 'assistant_final';
```

## 执行顺序

1. 停止旧二进制；
2. 执行本目录对应数据库的 SQL；
3. 启动新二进制。

语句幂等，可重复执行。执行后 `agent_messages` 中不再有 `assistant_final`。

## 前端影响

- 会话详情接口返回的消息 kind 只剩 `assistant`，无需再区分 `assistant_final`；
- 若升级时跳过本迁移，旧行仍能在 UI 中渲染（未知 kind 走默认文本渲染），
  但会被排除在下一轮 prompt 历史之外，因此务必执行。
