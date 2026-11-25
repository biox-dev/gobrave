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
	// ProjectID 是任务所属项目（创建时由调用方提供），0 表示未知。
	ProjectID int64 `json:"project_id,string,omitempty" gorm:"column:project_id;type:bigint;index"`
	// UserID 是任务归属用户（0 表示后台任务 / 无登录用户）。
	UserID   int64  `json:"user_id,string,omitempty" gorm:"column:user_id;type:bigint;index"`
	Provider string `json:"provider" gorm:"column:provider;type:varchar(64)"`
	Model    string `json:"model,omitempty" gorm:"column:model;type:varchar(128)"`
	// ResolvedProfileID 是本轮实际使用的 Agent Profile ID（已按 0 → 内置默认）解析），
	// 在 run 开始时写入（见 AgentService.run）。供工具在执行期定位当前 Profile。
	ResolvedProfileID int64      `json:"resolved_profile_id,string,omitempty" gorm:"column:resolved_profile_id;type:bigint"`
	Status            TaskStatus `json:"status" gorm:"column:status;type:varchar(32);index"`
	WorkingDir        string     `json:"working_dir,omitempty" gorm:"column:working_dir;type:text"`

	// Request 保存原始请求，用于恢复时重建上下文。
	Request Request `json:"request,omitempty" gorm:"serializer:json"`
	// Resumable 是非持久化派生字段：任务处于中断态（failed / canceled /
	// waiting_permission）时为真，前端据此在轮末展示「Continue」。
	//
	// 它能成立是因为恢复所需的事实（assistant 轮次 / 工具调用 / 工具结果）全部落在
	// agent_messages：原生循环可以跳过已完成的工具精确续跑，其它 Provider 则重跑本请求。
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

// AfterFind 在从数据库加载任务后派生 Resumable 字段，前端无需感知中断细节。
func (t *Task) AfterFind(*gorm.DB) error {
	t.Resumable = t.Status.Resumable()
	return nil
}

// Resumable 判断任务是否处于可续跑的中断态。
//
// completed / created 不属于中断态：前者已正常结束，后者应走 StartTask。
// 是否真的还有工具需要重放由 Provider 从 agent_messages 自行判断。
func (s TaskStatus) Resumable() bool {
	switch s {
	case TaskFailed, TaskCanceled, TaskWaitingPermission:
		return true
	default:
		return false
	}
}

// TaskFacts 是一次 Agent 执行所需的只读任务事实快照。
//
// 与 *Task 不同，它不含 Status / 时间戳等可变状态：
// 因此可以安全地被 Runtime 持有并跨 goroutine 读取，也不会让 Agent 层依赖持久化实体。
// 「谁在执行」这类事实（任务 / 会话 / 项目 / 用户）与工作目录统一从这里读取。
type TaskFacts struct {
	TaskID     int64
	SessionID  int64
	ProjectID  int64
	UserID     int64
	WorkingDir string
	// ProfileID 是本轮任务使用的 Agent Profile ID（0 表示未显式指定，
	// 即使用内置默认 Profile）。供 update_profile_prompt 等工具定位当前 Profile。
	ProfileID int64
}

// Facts 返回任务的只读事实快照；nil 任务返回零值。
func (t *Task) Facts() TaskFacts {
	if t == nil {
		return TaskFacts{}
	}
	return TaskFacts{
		TaskID:     t.ID,
		SessionID:  t.SessionID,
		ProjectID:  t.ProjectID,
		UserID:     t.UserID,
		WorkingDir: t.WorkingDir,
		ProfileID:  t.ResolvedProfileID,
	}
}

// NewTask 基于请求与执行事实构建一个 created 状态的任务。
//
// 执行事实（用户 / 会话 / 项目）由调用方显式提供：它们是任务的生命周期属性，
// 不再散落在 Request / RuntimeContext 里。
func NewTask(req Request, facts TaskFacts) *Task {
	now := time.Now()
	return &Task{
		// TaskID 由调用方预先分配时直接复用（user 消息先于任务写入，必须共用同一 ID）；
		// 为 0 时由 BeforeCreate 生成雪花 ID。
		ID:                facts.TaskID,
		SessionID:         facts.SessionID,
		ProjectID:         facts.ProjectID,
		UserID:            facts.UserID,
		Provider:          req.Provider,
		Model:             req.ModelID,
		ResolvedProfileID: req.ProfileID,
		Status:            TaskCreated,
		WorkingDir:        facts.WorkingDir,
		Request:           req,
		CreatedAt:         now,
		UpdatedAt:         now,
		Resumable:         false,
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
// 允许 failed / canceled / waiting_permission / running（无活跃 goroutine 的挂起态）续跑；
// completed / created 不允许通过该入口恢复（created 走 StartTask）。
// 用户手动终止（canceled）同样属于可恢复的中断态。
// 与 TransitionTo 分开是为了保持常规状态机的严格性：failed/canceled → running 只允许
// 在「人工点击继续 / 权限审批」这一条路径上发生。
func (t *Task) MarkResumed() bool {
	switch t.Status {
	case TaskFailed, TaskCanceled, TaskWaitingPermission, TaskRunning:
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
