可以。你现在这个 `Agent` 抽象已经有了一个很好的基础，但如果要支持：

* Agent 执行过程中产生 `PermissionRequest`
* `read` 可以自动允许，`write/delete/exec` 需要 UI 确认
* 后端通过 WebSocket 通知前端
* 浏览器刷新后，待确认操作仍然存在
* 后端重启后，Agent/任务状态可以恢复
* 前端确认/拒绝之后，Agent 继续执行
* Claude Code / Codex / Copilot / 自研 Agent 都统一处理

那么我建议**不要把 Permission 做成 `StreamEvent` 的一个普通事件然后丢给前端**。

核心思想应该是：

> **Agent 是执行层，Operation/Permission 是持久化的业务层，WS 只是实时通知层。数据库才是真正的状态源。**

整体架构可以设计成下面这样。

---

# 1. 推荐的整体架构

```text
                    Browser
                       │
             ┌─────────┴─────────┐
             │                   │
          REST/HTTP              WS
             │                   │
             ▼                   ▼
      ┌────────────────────────────────┐
      │          Agent Service         │
      │                                │
      │  ChatHandler                   │
      │  AgentTaskManager              │
      │  PermissionManager             │
      │  EventManager                  │
      └───────────────┬────────────────┘
                      │
             ┌────────┴────────┐
             │                 │
             ▼                 ▼
         PostgreSQL          Redis
        / MySQL/SQLite       optional
             │
             │
             ▼
      Durable State
      ─────────────
      AgentTask
      AgentEvent
      PermissionRequest
      AgentMessage
      Operation
             │
             ▼
      ┌───────────────────┐
      │    Agent Client   │
      │                   │
      │   Client          │
      │      │            │
      │   Registry        │
      │      │            │
      │ ┌────┼─────┐      │
      │ ▼    ▼     ▼      │
      │Claude Codex Copilot│
      └───────────────────┘
```

这里最重要的是：

```text
                    ┌─────────────┐
                    │   Database  │
                    │             │
                    │ Source of   │
                    │ Truth       │
                    └──────┬──────┘
                           │
                ┌──────────┴──────────┐
                │                     │
          Agent execution         Browser UI
```

而不是：

```text
Agent ──WS──> Browser
```

因为后者**无法可靠恢复**。

---

# 2. 不要让 PermissionRequest 直接依赖 WS

比如 Claude Code 产生：

```text
我要修改 /workspace/main.go
```

Agent 不应该直接：

```go
ws.Send(PermissionRequest)
```

而应该：

```text
Agent
  │
  ▼
PermissionManager
  │
  ├── DB.create(PermissionRequest)
  │
  └── EventBus.Publish(PermissionCreated)
                 │
                 ▼
                WS
                 │
                 ▼
              Browser
```

也就是说：

> **WS 是 notification，不是 state。**

这是整个设计最重要的一点。

---

# 3. 建议把 Permission 从 StreamEvent 中独立出来

你现在：

```go
type StreamEventType string

const (
    StreamEventText
    StreamEventReasoning
    StreamEventToolCall
    StreamEventToolResult
    StreamEventDone
    StreamEventError
)
```

可以继续保留。

但是我建议增加：

```go
StreamEventPermissionRequest
```

只是**事件通知**。

真正的数据结构独立出来：

```go
type PermissionRequest struct {
    ID        string
    TaskID    string
    SessionID string

    Operation Operation

    Status PermissionStatus

    CreatedAt time.Time
    UpdatedAt time.Time

    ResolvedAt *time.Time
    ResolvedBy *string
}
```

例如：

```go
type Operation struct {
    Type string `json:"type"`

    // read / write
    Path string `json:"path"`

    // write
    Content string `json:"content,omitempty"`

    // exec
    Command string `json:"command,omitempty"`
}
```

状态：

```go
type PermissionStatus string

const (
    PermissionPending  PermissionStatus = "pending"
    PermissionApproved PermissionStatus = "approved"
    PermissionDenied   PermissionStatus = "denied"
    PermissionExpired  PermissionStatus = "expired"
    PermissionCanceled PermissionStatus = "canceled"
)
```

---

# 4. Permission 的核心生命周期

比如 Agent 想修改：

```text
/workspace/foo.py
```

整个过程应该是：

```text
Claude Code
     │
     │ write /workspace/foo.py
     ▼
Agent Adapter
     │
     ▼
PermissionManager
     │
     │ BEGIN TRANSACTION
     │
     ├── INSERT permission_request
     │       status = pending
     │
     └── COMMIT
            │
            ▼
        EventBus
            │
            ▼
           WS
            │
            ▼
         Browser
            │
       ┌────┴─────┐
       │          │
    Approve      Deny
       │          │
       └────┬─────┘
            │
            ▼
     PermissionManager
            │
            ▼
        DB UPDATE
            │
            ▼
       Agent Resume
```

注意：

**必须先写 DB，再通知 WS。**

不能：

```text
WS → DB
```

应该：

```text
DB → EventBus → WS
```

这样即使 WS 断掉：

```text
Browser
   X
   │
   │
Backend
   │
   ▼
Database
```

Permission 依然存在。

---

# 5. 浏览器刷新为什么可以恢复？

例如：

```text
task_123
```

当前有：

```text
permission_001
status = pending
```

浏览器刷新。

新的 WS 建立：

```text
Browser
   │
   │ WS connect
   ▼
Backend
   │
   ▼
PermissionManager
   │
   ▼
SELECT *
FROM permission_request
WHERE task_id = 'task_123'
AND status = 'pending'
   │
   ▼
Browser
```

所以 UI 重新显示：

```text
┌──────────────────────────────────────┐
│ Agent wants to modify file           │
│                                      │
│ /workspace/main.py                   │
│                                      │
│ [ View Diff ]                        │
│                                      │
│       [Deny]       [Allow]           │
└──────────────────────────────────────┘
```

这里根本不依赖之前那个 WS 消息。

---

# 6. 后端重启也是一样

例如：

```text
22:00 Agent running

22:01 Agent:
      PermissionRequest #100
      write foo.py

22:01 DB:
      #100 pending

22:02 Backend crash

22:03 Backend restart
```

启动的时候：

```text
AgentService.Start()
       │
       ├── Load pending tasks
       │
       ├── Load pending permissions
       │
       └── Resume tasks
```

数据库：

```text
task_123
status = running

permission_100
status = pending
```

系统就知道：

```text
task_123
   │
   └── waiting for permission_100
```

然后重新启动对应 Agent execution。

---

# 7. 这里其实需要两个状态机

这是设计中非常重要的一点。

不要只有：

```go
Task.Status
```

应该至少有：

## Task 状态

```text
created
   │
   ▼
running
   │
   ├───────────────┐
   │               │
   ▼               ▼
waiting_permission failed
   │
   ▼
running
   │
   ▼
completed
```

例如：

```go
type TaskStatus string

const (
    TaskCreated           TaskStatus = "created"
    TaskRunning           TaskStatus = "running"
    TaskWaitingPermission TaskStatus = "waiting_permission"
    TaskCompleted         TaskStatus = "completed"
    TaskFailed             TaskStatus = "failed"
    TaskCanceled           TaskStatus = "canceled"
)
```

---

# 8. Permission 自己也有状态机

```text
              ┌───────────┐
              │  pending  │
              └─────┬─────┘
                    │
             ┌──────┴──────┐
             │             │
             ▼             ▼
         approved        denied
             │
             ▼
          consumed
```

所以：

```text
Task
 │
 │ waiting_permission
 ▼
PermissionRequest
 │
 │ pending
 │
 ├──── approve ────> Agent Resume
 │
 └──── deny ───────> Agent Error/Continue
```

---

# 9. Agent 接口最好进一步扩展

你现在：

```go
type Agent interface {
    Name() string

    Invoke(
        ctx context.Context,
        req Request,
    ) (*Result, error)

    Stream(
        ctx context.Context,
        req Request,
        handler StreamHandler,
    ) (*Result, error)
}
```

如果要支持 Permission，我不建议简单增加：

```go
OnPermissionRequest(...)
```

因为这样 Provider 会越来越依赖上层业务。

更好的方式是引入：

```go
type AgentCallbacks struct {
    OnPermissionRequest PermissionHandler
    OnEvent             EventHandler
}
```

例如：

```go
type PermissionHandler func(
    ctx context.Context,
    req PermissionRequest,
) (PermissionDecision, error)
```

然后：

```go
type AgentRuntime struct {
    StreamHandler     StreamHandler
    PermissionHandler PermissionHandler
}
```

Agent：

```go
type Agent interface {
    Name() string

    Invoke(
        ctx context.Context,
        req Request,
        runtime Runtime,
    ) (*Result, error)

    Stream(
        ctx context.Context,
        req Request,
        runtime Runtime,
    ) (*Result, error)
}
```

---

# 10. 但是还有一个更关键的问题

如果你的 Agent 是：

```text
Claude Code CLI
Codex CLI
Copilot CLI
```

那么 Permission 实际上不是简单的：

```go
OnPermissionRequest(...)
```

因为真正的 Agent 进程可能是：

```text
Backend
   │
   ▼
Claude CLI
   │
   │ "Can I write foo.go?"
   ▼
Backend
```

你需要把 Provider Adapter 做成一个**Agent Runtime Adapter**。

例如：

```text
                 Agent Client
                      │
              ┌───────┴────────┐
              │                 │
        ClaudeCodeAgent     CodexAgent
              │                 │
              ▼                 ▼
          CLI Process       CLI Process
              │                 │
              └────────┬────────┘
                       │
                 AgentRuntime
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
       Event        Permission    Process
       Handler       Handler       Manager
```

---

# 11. 最推荐的核心抽象：Operation

我甚至建议不要叫：

```go
OnPermissionRequest
```

作为整个体系的核心。

而是：

```go
Operation
```

因为以后不只有：

```text
read
write
```

还会有：

```text
read
write
delete
move
execute
network
install_package
git_push
docker
kubernetes
```

例如：

```go
type OperationType string

const (
    OperationRead            OperationType = "read"
    OperationWrite           OperationType = "write"
    OperationDelete          OperationType = "delete"
    OperationMove            OperationType = "move"
    OperationExecute         OperationType = "execute"
    OperationNetwork         OperationType = "network"
)
```

然后：

```go
type Operation struct {
    ID       string
    Type     OperationType

    Path     string
    Command  string
    Content  string

    Metadata map[string]any
}
```

---

# 12. PermissionPolicy

然后再有一个：

```go
type PermissionPolicy interface {
    Check(
        ctx context.Context,
        op Operation,
    ) PermissionDecision
}
```

例如：

```go
type PermissionDecision string

const (
    DecisionAllow PermissionDecision = "allow"
    DecisionDeny  PermissionDecision = "deny"
    DecisionAsk   PermissionDecision = "ask"
)
```

那么：

```text
Agent
  │
  ▼
Operation
  │
  ▼
PermissionPolicy
  │
  ├── read foo.txt
  │       │
  │       └── allow
  │
  ├── write foo.txt
  │       │
  │       └── ask
  │
  └── rm -rf xxx
          │
          └── deny
```

这比：

```go
OnPermissionRequest()
```

更容易扩展。

---

# 13. 一个完整调用链

最终我建议你的架构是：

```text
                    Browser
                       │
             ┌─────────┴─────────┐
             │                   │
            HTTP                 WS
             │                   │
             ▼                   ▼
       AgentTaskHandler    WebSocketHub
             │                   ▲
             ▼                   │
        AgentService             │
             │                   │
             ├───────────────────┤
             │                   │
             ▼                   │
       AgentTaskManager          │
             │                   │
             ▼                   │
        Agent Runtime            │
             │                   │
             ▼                   │
        Agent Client             │
             │                   │
      ┌──────┼────────┐          │
      ▼      ▼        ▼          │
   Claude   Codex   Copilot      │
      │      │        │          │
      └──────┼────────┘          │
             │                   │
             ▼                   │
         Operation                │
             │                   │
             ▼                   │
      PermissionPolicy           │
             │                   │
       ┌─────┼──────┐            │
       ▼     ▼      ▼            │
     allow   ask   deny          │
             │                   │
             ▼                   │
    PermissionManager            │
             │                   │
             ▼                   │
          Database               │
             │                   │
             ▼                   │
         EventBus ───────────────┘
```

---

# 14. 数据库建议这样拆

至少：

```text
agent_task
agent_event
agent_message
permission_request
operation
```

例如：

### agent_task

```text
id
session_id
provider
model
status
working_dir
created_at
updated_at
started_at
finished_at
```

### agent_event

```text
id
task_id
sequence
type
payload
created_at
```

这里的：

```text
sequence
```

非常重要。

例如：

```text
1 text
2 text
3 tool_call
4 permission_request
5 permission_resolved
6 tool_result
7 text
8 done
```

这样浏览器刷新后甚至可以：

```text
GET /tasks/123/events?after=3
```

继续恢复事件。

---

# 15. Permission 表

```text
permission_request

id
task_id
operation_id

status

request_payload
decision_payload

created_at
updated_at
resolved_at
resolved_by
```

例如：

```json
{
  "id": "perm_123",
  "task_id": "task_001",
  "status": "pending",
  "operation": {
    "type": "write",
    "path": "/workspace/main.py",
    "content": "..."
  }
}
```

---

# 16. WS 不应该负责恢复历史

例如：

```text
WS event:

permission.created
```

只是：

> “数据库里面刚刚多了一条 Permission。”

前端收到：

```json
{
  "type": "permission.created",
  "id": "perm_123"
}
```

然后 UI 可以更新。

但是刷新之后：

```text
WS connect
     │
     ▼
GET /tasks/task_001
     │
     ▼
pending_permissions
```

或者：

```text
WS connect
     │
     ▼
Backend 查询 DB
     │
     ▼
snapshot
```

例如：

```json
{
  "type": "task.snapshot",
  "task": {},
  "pending_permissions": []
}
```

这才是真正可靠的设计。

---

# 17. Permission approve 的调用链

前端：

```http
POST /permissions/perm_123/approve
```

后端：

```text
PermissionHandler
       │
       ▼
PermissionManager.Approve()
       │
       ├── DB transaction
       │      status = approved
       │
       ├── EventBus.Publish()
       │
       └── notify AgentTaskManager
                       │
                       ▼
                 waiting Agent
                       │
                       ▼
                  resume()
```

WS 同时：

```text
Backend
   │
   ▼
WebSocketHub
   │
   ▼
Browser
```

所以 HTTP 是：

> **command**

WS 是：

> **notification**

DB 是：

> **state**

这是非常清晰的 CQRS 风格。

---

# 18. Agent 为什么能够“暂停等待”？

这里有一个实现上的关键点。

你的 Agent：

```go
Stream(...)
```

不能简单：

```go
PermissionRequest(...)
return
```

否则 Agent 就结束了。

需要：

```go
permissionCh := make(chan PermissionDecision, 1)

permissionID := permissionManager.Create(...)

runtime.WaitPermission(
    ctx,
    permissionID,
)
```

内部：

```text
Agent Process
     │
     ▼
Permission Request
     │
     ▼
DB pending
     │
     ▼
等待 channel
     │
     │
     │   10 秒
     │   1 分钟
     │   1 小时
     │
     ▼
Approve
     │
     ▼
channel <- approved
     │
     ▼
Agent continue
```

但这里还有一个问题：

**Go channel 本身不能持久化。**

所以：

```go
permissionCh
```

只是运行时同步机制。

真正恢复依赖：

```text
DB
```

---

# 19. 重启后的恢复

比如：

```text
Agent Task
task_001

status = waiting_permission

permission_001
status = pending
```

Backend 重启：

```text
main()
 │
 ▼
AgentService.Start()
 │
 ▼
RecoveryManager.Recover()
 │
 ├── 查询 running task
 ├── 查询 waiting_permission task
 └── 查询 pending permission
```

然后：

```text
task_001
   │
   ▼
permission_001 pending
   │
   ▼
重建 Runtime
```

Agent 是否可以直接继续执行，取决于 Provider。

---

# 20. 这里有一个非常重要的现实问题

如果：

```text
Claude CLI
```

本身是一个外部进程。

Backend 崩溃以后：

```text
Backend ❌
Claude CLI ?
```

那么你必须决定：

### 方案 A：Agent process 也跟着 Backend 生命周期

```text
Backend crash
    ↓
Agent process killed
```

重启后根据：

```text
task state
```

重新启动 Agent。

### 方案 B：Agent execution 独立进程

例如：

```text
AgentTaskManager
       │
       ▼
Agent Worker
       │
       ▼
Claude CLI
```

Worker 独立于 API Server。

这是我**更推荐**的。

---

# 21. 最终可以拆成三个进程

大型系统：

```text
                 Browser
                    │
              API Server
              /         \
             /           \
           REST           WS
             │             │
             ▼             ▼
        PostgreSQL      EventBus
             ▲             ▲
             │             │
             └──────┬──────┘
                    │
               Agent Worker
                    │
              ┌─────┼─────┐
              ▼     ▼     ▼
           Claude Codex Copilot
```

API Server：

```text
创建任务
查询任务
Approve/Deny
WS
```

Agent Worker：

```text
真正执行 Agent
处理 stdout
处理 tool call
等待 Permission
恢复 Agent
```

数据库：

```text
所有 durable state
```

EventBus：

```text
实时通知
```

---

# 22. 如果你暂时不想拆 Worker

你的 BRAVE 这种项目早期完全可以：

```text
FastAPI / Go Server
       │
       ├── AgentTaskManager
       │
       ├── PermissionManager
       │
       ├── EventManager
       │
       └── Agent Client
              │
              ├── Claude
              ├── Codex
              └── Copilot
```

先做到：

```text
DB durable
+
WS notification
+
Task recovery
```

以后再把：

```text
AgentTaskManager
```

抽出去成为：

```text
agent-worker
```

接口基本不用变化。

---

# 23. 我会建议你的 Go package 最终这样组织

```text
agent/
├── agent.go
├── client.go
├── registry.go
│
├── runtime/
│   ├── runtime.go
│   ├── event.go
│   └── permission.go
│
├── operation/
│   ├── operation.go
│   └── policy.go
│
├── task/
│   ├── task.go
│   ├── manager.go
│   └── recovery.go
│
├── permission/
│   ├── permission.go
│   ├── manager.go
│   └── repository.go
│
├── event/
│   ├── event.go
│   ├── bus.go
│   └── repository.go
│
├── provider/
│   ├── claude/
│   ├── codex/
│   ├── copilot/
│   └── mock/
│
└── persistence/
    ├── task_repository.go
    ├── permission_repository.go
    └── event_repository.go
```

---

# 24. 最核心的几个接口

如果让我在你现在的代码基础上继续设计，我会优先形成这几个核心接口：

```go
type Agent interface {
    Name() string

    Invoke(
        ctx context.Context,
        req Request,
        runtime Runtime,
    ) (*Result, error)

    Stream(
        ctx context.Context,
        req Request,
        runtime Runtime,
    ) (*Result, error)
}
```

```go
type Runtime interface {
    Emit(ctx context.Context, event StreamEvent) error

    RequestPermission(
        ctx context.Context,
        operation Operation,
    (PermissionDecision, error)

    WaitPermission(
        ctx context.Context,
        permissionID string,
    ) (PermissionDecision, error)
}
```

```go
type PermissionManager interface {
    Create(
        ctx context.Context,
        taskID string,
        operation Operation,
    ) (*PermissionRequest, error)

    Approve(
        ctx context.Context,
        permissionID string,
    ) error

    Deny(
        ctx context.Context,
        permissionID string,
    ) error

    GetPending(
        ctx context.Context,
        taskID string,
    ) ([]PermissionRequest, error)
}
```

```go
type EventBus interface {
    Publish(ctx context.Context, event Event) error

    Subscribe(
        ctx context.Context,
        taskID string,
        handler EventHandler,
    ) error
}
```

---

# 25. 最终最重要的一张图

你的整个 Agent 系统，我建议最终遵循这个原则：

```text
                     ┌──────────────┐
                     │   Browser    │
                     └──────┬───────┘
                            │
                     REST / WebSocket
                            │
                            ▼
                  ┌───────────────────┐
                  │    API Server     │
                  │                   │
                  │ TaskManager       │
                  │ PermissionManager │
                  └─────────┬─────────┘
                            │
                    ┌───────┴────────┐
                    │                │
                    ▼                ▼
              ┌──────────┐      ┌──────────┐
              │ Database │      │ EventBus │
              │          │      │          │
              │ SOURCE   │      │ REALTIME │
              │ OF TRUTH │      │ NOTIFY   │
              └────┬─────┘      └────┬─────┘
                   │                 │
                   │                 │
                   └───────┬─────────┘
                           ▼
                    ┌──────────────┐
                    │ Agent Worker │
                    │              │
                    │ Runtime      │
                    │ Permission   │
                    │ Event        │
                    └──────┬───────┘
                           │
                    ┌──────┼──────┐
                    ▼      ▼      ▼
                 Claude   Codex  Copilot
```

**一句话概括：**

> **Agent 负责“执行”，Operation 描述“要做什么”，PermissionManager 决定“能不能做”，Database 保存“现在是什么状态”，EventBus/WS 负责“告诉 UI 状态发生了变化”，RecoveryManager 负责“重启后重新建立运行态”。**

尤其是你前面考虑的 **SSE/WS + 浏览器刷新恢复** 场景，我会把 **SSE/WS 完全降级成“实时传输层”**，绝对不要让它承担 Agent 状态。这样即使浏览器刷新、WS 断线、API Server 重启，`pending permission`、Agent task、event sequence 都不会丢。

如果进一步往下落地，下一步最值得设计的是 **`Runtime + PermissionManager + AgentTask` 三个接口之间的 Go 代码**，尤其是“Agent 阻塞等待用户确认，但 Backend 重启后还能恢复”的那部分，这是整个架构最关键的地方。
