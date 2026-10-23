package types

import (
	"context"
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
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
	// ErrTaskNotResumable 表示任务没有可恢复检查点，或当前状态不允许续跑。
	ErrTaskNotResumable = errors.New("agent: task not resumable")
	// ErrInvalidTaskTransition 表示非法的任务状态迁移。
	ErrInvalidTaskTransition = errors.New("agent: invalid task state transition")
)

// TaskFinishedHandler 是任务进入终态（completed / failed / canceled）后的回调。
//
// 供上层编排层（如 ChatService）把「轮次收尾」绑定到任务状态机，而不是依赖
// Provider 是否补发了 done / error 流事件：Provider 少发一个事件不应导致会话锁泄漏。
//
// 回调在 transitionTask 写完库、广播终态事件之前同步执行，实现方应自行保证幂等
// （同一任务可能因重试 / 重复收尾被多次调用）。
type TaskFinishedHandler func(ctx context.Context, task *Task)

// Task 是一次 Agent 调用的持久化对象。
//
// 它与 PermissionRequest 形成两级状态机：Task 记录“整体执行到哪一步”，
// PermissionRequest 记录“某个具体操作是否被允许”。后端重启后，可根据 Task.Status
// 与 pending 的 PermissionRequest 重建运行态（见 Recovery）。
type Task struct {
	ID        int64 `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	SessionID int64 `json:"session_id,string,omitempty" gorm:"column:session_id;type:bigint;index"`
	// ProjectID 是任务所属项目（创建时取 Request.RuntimeContext.ProjectID），0 表示未知。
	ProjectID  int64      `json:"project_id,string,omitempty" gorm:"column:project_id;type:bigint;index"`
	Provider   string     `json:"provider" gorm:"column:provider;type:varchar(64)"`
	Model      string     `json:"model,omitempty" gorm:"column:model;type:varchar(128)"`
	Status     TaskStatus `json:"status" gorm:"column:status;type:varchar(32);index"`
	WorkingDir string     `json:"working_dir,omitempty" gorm:"column:working_dir;type:text"`

	// Request 保存原始请求，用于恢复时重建上下文。
	Request Request `json:"request,omitempty" gorm:"serializer:json"`
	// ResumeState 是可恢复执行检查点（Agent 循环的对话历史 / 待执行工具调用）。
	// 为空表示该任务不可续跑（例如 Provider 未实现检查点，或任务从未开始执行）。
	// 该列只由 TaskRepository.UpdateResumeState 维护，普通 Update 会显式 Omit，
	// 避免内存中的旧 Task 快照在状态迁移时把检查点覆盖成 NULL。
	ResumeState *TaskResumeState `json:"-" gorm:"column:resume_state;type:longtext;serializer:json"`
	// Resumable 是非持久化派生字段：只要存在检查点就可以被手动 / 自动恢复。
	Resumable bool `json:"resumable" gorm:"-"`
	// Error 保存最近一次错误信息（Status == failed 时有效）。
	Error string `json:"error,omitempty" gorm:"column:error;type:text"`

	CreatedAt  time.Time  `json:"created_at" gorm:"column:created_at"`
	UpdatedAt  time.Time  `json:"updated_at" gorm:"column:updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty" gorm:"column:started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty" gorm:"column:finished_at"`
}

// TableName 返回任务表的表名。
func (Task) TableName() string { return "agent_tasks" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (t *Task) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// AfterFind 在从数据库加载任务后派生 Resumable 字段，前端无需感知检查点细节。
func (t *Task) AfterFind(*gorm.DB) error {
	t.Resumable = t.ResumeState != nil
	return nil
}

// NewTask 基于请求构建一个 created 状态的任务。
func NewTask(req Request) *Task {
	now := time.Now()
	// 任务归属项目来自运行时上下文（用户当前激活项目），随请求一起解析。
	var projectID int64
	if req.RuntimeContext != nil {
		projectID = req.RuntimeContext.ProjectID
	}
	return &Task{
		SessionID:   req.SessionID,
		ProjectID:   projectID,
		Provider:    req.Provider,
		Model:       req.ModelID,
		Status:      TaskCreated,
		WorkingDir:  req.WorkingDir,
		Request:     req,
		CreatedAt:   now,
		UpdatedAt:   now,
		Resumable:   false,
		ResumeState: nil,
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

// MarkResumed 把任务从「中断态」重新置为 running。
//
// 只允许 failed / waiting_permission / running（无活跃 goroutine 的挂起态）续跑；
// completed / canceled / created 不允许通过该入口恢复（created 走 StartTask）。
// 与 TransitionTo 分开是为了保持常规状态机的严格性：failed → running 只允许
// 在“存在持久化检查点且人工 / 权限审批触发恢复”这一条路径上发生。
func (t *Task) MarkResumed() bool {
	switch t.Status {
	case TaskFailed, TaskWaitingPermission, TaskRunning:
		now := time.Now()
		t.Status = TaskRunning
		t.Error = ""
		t.FinishedAt = nil
		t.UpdatedAt = now
		if t.StartedAt == nil {
			t.StartedAt = &now
		}
		return true
	default:
		return false
	}
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
