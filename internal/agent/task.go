package agent

import (
	"errors"
	"time"
)

// TaskStatus 是 Agent 任务的生命周期状态。
//
// 状态机（详见 design.md 第 7 节）：
//
//	created → running → completed
//	               ↘ failed / canceled
//	running → waiting_permission → running
type TaskStatus string

const (
	TaskCreated           TaskStatus = "created"            // 已创建，尚未开始
	TaskRunning           TaskStatus = "running"            // 正在执行
	TaskWaitingPermission TaskStatus = "waiting_permission" // 等待权限确认
	TaskCompleted         TaskStatus = "completed"          // 已完成
	TaskFailed            TaskStatus = "failed"             // 失败
	TaskCanceled          TaskStatus = "canceled"           // 已取消
)

// 任务相关错误。
var (
	// ErrTaskNotFound 表示任务不存在。
	ErrTaskNotFound = errors.New("agent: task not found")
	// ErrTaskAlreadyRunning 表示任务已经在执行中。
	ErrTaskAlreadyRunning = errors.New("agent: task already running")
	// ErrInvalidTaskTransition 表示非法的任务状态迁移。
	ErrInvalidTaskTransition = errors.New("agent: invalid task state transition")
)

// Task 是一次 Agent 调用的持久化对象。
//
// 它与 PermissionRequest 形成两级状态机：Task 记录“整体执行到哪一步”，
// PermissionRequest 记录“某个具体操作是否被允许”。后端重启后，可根据 Task.Status
// 与 pending 的 PermissionRequest 重建运行态（见 Recovery）。
type Task struct {
	ID         string     `json:"id"`
	SessionID  string     `json:"session_id,omitempty"`
	Provider   string     `json:"provider"`
	Model      string     `json:"model,omitempty"`
	Status     TaskStatus `json:"status"`
	WorkingDir string     `json:"working_dir,omitempty"`

	// Request 保存原始请求，用于恢复时重建上下文。
	Request Request `json:"request,omitempty"`
	// Error 保存最近一次错误信息（Status == failed 时有效）。
	Error string `json:"error,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// NewTask 基于请求构建一个 created 状态的任务。
func NewTask(req Request) *Task {
	now := time.Now()
	return &Task{
		ID:         newID("task"),
		SessionID:  req.SessionID,
		Provider:   req.Provider,
		Model:      req.Model,
		Status:     TaskCreated,
		WorkingDir: req.WorkingDir,
		Request:    req,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// TransitionTo 校验并执行任务状态迁移；非法迁移返回 false 且不改变状态。
func (t *Task) TransitionTo(next TaskStatus) bool {
	if !canTransitionTask(t.Status, next) {
		return false
	}
	t.Status = next
	t.UpdatedAt = time.Now()
	return true
}

func canTransitionTask(from, to TaskStatus) bool {
	switch from {
	case TaskCreated:
		return to == TaskRunning || to == TaskCanceled || to == TaskFailed
	case TaskRunning:
		return to == TaskWaitingPermission ||
			to == TaskCompleted ||
			to == TaskFailed ||
			to == TaskCanceled
	case TaskWaitingPermission:
		return to == TaskRunning ||
			to == TaskCompleted ||
			to == TaskFailed ||
			to == TaskCanceled
	default:
		// completed / failed / canceled 均为终态。
		return false
	}
}
