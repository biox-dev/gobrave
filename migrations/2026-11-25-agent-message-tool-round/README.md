# Agent 消息增加轮次 / 工具调用标识

给 `agent_messages` 增加 `turn_id` 与 `tool_call_id` 两列（含索引），作为
「任务中断后续跑」的唯一事实来源。

## 背景

旧实现把恢复现场存成 `agent_tasks.resume_state`（一整段 JSON：完整对话历史 +
待执行工具）。它和时间线 `agent_messages` 里已经有一份对话，等于同一事实存两份，
而且每完成一个工具就要把整段历史重写一次，token 越长写放大越严重；同时只有原生
Provider 写检查点，其它 Provider 无法续跑。

新实现只保留一个事实源：`agent_messages`。

- `turn_id`：产生该消息的模型响应轮次。同一次响应的 assistant 文本 / `tool_call` /
  `tool_result` 共享它，恢复时据此把消息归为一轮。
- `tool_call_id`：把 `tool_call` 与对应 `tool_result` 配对。

Agent 循环恢复时按 `turn_id` 分组，用「本轮调用集合 − 已落库结果集合」得到还需要
重放的调用；已经有结果的工具直接复用，不再执行。因此：

- 正常重启不会重复执行已完成的工具；
- 非幂等操作（写文件 / 执行命令）在「工具已执行、结果尚未落库」的窗口内崩溃时会被
  再次执行，仍是 at-least-once 语义。

## 变更

```sql
ALTER TABLE agent_messages ADD COLUMN turn_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE agent_messages ADD COLUMN tool_call_id VARCHAR(128) NOT NULL DEFAULT '';
CREATE INDEX idx_agent_messages_turn_id ON agent_messages (turn_id);
CREATE INDEX idx_agent_messages_tool_call_id ON agent_messages (tool_call_id);
```

`agent_tasks.resume_state` 列不再被代码读写，可以保留（默认 NULL，不占额外空间）
或按需手工删除：

```sql
ALTER TABLE agent_tasks DROP COLUMN resume_state;
```

## 执行顺序

1. 停止旧二进制；
2. 执行本目录对应数据库的 SQL；
3. 启动新二进制。

## 前端影响

无。新增列不改变消息接口的既有字段；`turn_id` / `tool_call_id` 为空时不出现在 JSON 中。
