package types

import (
	"context"
	"time"
)

// AgentEventType 枚举任务生命周期与实时通知事件。
//
// 流式内容事件统一使用 EventStream，具体细分见 StreamEvent.Type；
// 权限与任务状态变化使用独立类型，便于前端按类型订阅与恢复。
type AgentEventType string

const (
	EventTaskCreated        AgentEventType = "task.created"        // 任务已创建
	EventTaskStarted        AgentEventType = "task.started"        // 任务开始执行
	EventTaskWaiting        AgentEventType = "task.waiting"        // 任务进入等待权限状态
	EventTaskCompleted      AgentEventType = "task.completed"      // 任务完成
	EventTaskFailed         AgentEventType = "task.failed"         // 任务失败
	EventTaskCanceled       AgentEventType = "task.canceled"       // 任务取消
	EventPermissionCreated  AgentEventType = "permission.created"  // 新增待确认权限
	EventPermissionResolved AgentEventType = "permission.resolved" // 权限已被批准 / 拒绝
	EventQuestionCreated    AgentEventType = "question.created"    // 新增待作答提问（Payload 为 QuestionRequest）
	EventQuestionResolved   AgentEventType = "question.resolved"   // 提问已作答 / 取消（Payload 为 QuestionRequest）
	EventMemorySaved        AgentEventType = "memory.saved"        // 记忆已保存（Payload 为 Memory）
	EventMemoryDeleted      AgentEventType = "memory.deleted"      // 记忆已删除（Payload 为 Memory）
	EventStream             AgentEventType = "stream"              // 透传的流式事件（Payload 为 StreamEvent）
)

// AgentEvent 是发布到 EventBus 的实时通知事件。
//
// 它只承担实时推送职责（WS / SSE），**不落库**：任务时间线的持久化由
// TimelineRecorder 投影到 agent_messages（见 timeline.go）。
// 前端刷新 / 恢复历史请读会话消息，而不是事件流。
type AgentEvent struct {
	ID     int64 `json:"id,string"`
	TaskID int64 `json:"task_id,string"`
	// UserID / SessionID 是事件的推送路由与归属信息：由服务层从 TaskFacts 填充，
	// 传输层（WS / SSE）据此决定推给谁，不需要再维护 taskID → userID 的映射表，
	// 也不需要按任务逐个订阅。
	//
	// UserID 为 0 表示无登录用户（后台任务 / 系统摘要），不应推送给任何客户端。
	UserID    int64          `json:"user_id,string,omitempty"`
	SessionID int64          `json:"session_id,string,omitempty"`
	Type      AgentEventType `json:"type"`
	Payload   any            `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// EventHandler 是事件订阅回调。
// 返回 error 表示处理失败；EventBus 目前采用同步分发，回调应避免阻塞。
type EventHandler func(ctx context.Context, event AgentEvent) error
