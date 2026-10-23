# internal/agent 目录结构

```
internal/agent/
├── types/                     # 领域实体 + 值对象（无仓储/编排实现）
├── types/interface/           # Repository / Service / Runtime 等接口（package interfaces）
├── application/
│   ├── repository/            # Repository 的 GORM 实现
│   └── service/               # Service / Manager 实现
├── client/                    # Client / Registry / Options（Provider 门面）
├── runtime/                   # Standalone Runtime、ToolRunner/ToolLoop、工具上下文桥接
├── policy/                    # PermissionPolicy 实现
├── runtimecontext/            # RuntimeContext 及工具执行期服务能力接口
├── providers/                 # claude_code / codex / copilot / custom / mock
├── native/                    # 原生 Agent 循环
├── tool/                      # 工具注册表、执行器、builtin / coder
├── skill/                     # Agent Skills
├── llm/ lsp/ mcp/ logging/    # 基础设施
├── compat.go                  # 旧根包 agent 的兼容导入面（类型别名/构造器转发）
└── session/ message/ history/ # 旧导入路径兼容层（类型/接口别名）
```

## 分层约定

| 层 | 内容 | 位置 |
| --- | --- | --- |
| 实体 / 值对象 | `Task`、`Session`、`Message`、`PermissionRequest`、`QuestionRequest`、`Profile`、`Memory`、`File`、`Request`、`Result`、`StreamEvent` … | `types/` |
| 端口（接口） | `TaskRepository`、`PermissionRepository`、`QuestionRepository`、`MemoryRepository`、`ProfileRepository`、`SessionStore`、`MessageStore`、`HistoryStore`、`SessionService`、`MessageService`、`HistoryService`、`Agent`、`Runtime`、`CheckpointRuntime`、`EventBus`、`PermissionPolicy` … | `types/interface/`（package `interfaces`） |
| 持久化实现 | `NewGormTaskRepository`、`NewGormSessionStore`、`NewGormMessageStore`… | `application/repository/` |
| 服务实现 | `AgentService`、`ChatService`、`MemoryManager`、`PermissionManager`、`QuestionManager`、`ProfileManager`、`TimelineRecorder`、会话/消息/历史服务… | `application/service/` |
| 门面 / 运行时 | `Client`、`Registry`、`Options`、`ToolRunner`、`NewStandaloneRuntime` | `client/`、`runtime/` |

> 目录名 `types/interface` 是对外约定的路径；由于 `interface` 是 Go 关键字，
> 包声明使用 `package interfaces`。

## 兼容层

根包 `agent` 以及 `session` / `message` / `history` 保留了旧导入路径的类型别名与构造
函数转发，便于上层（`handler` / `container` / `manager` / `providers` / `native`）平滑迁移。
新代码应直接依赖：

- `github.com/biox-dev/gobrave/internal/agent/types`
- `github.com/biox-dev/gobrave/internal/agent/types/interface`
- `github.com/biox-dev/gobrave/internal/agent/application/service`
- `github.com/biox-dev/gobrave/internal/agent/application/repository`
