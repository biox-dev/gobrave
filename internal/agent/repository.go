package agent

import "context"

// 本文件定义持久化抽象（Repository 接口）。
//
// 设计原则（见 design.md）：数据库才是状态源，EventBus / WS 只是通知层。
// 因此 Repository 接口被设计为可替换的；具体实现见 internal/agent/repository 包
// （基于 GORM 的数据库实现）。
//
// 遵循「接口定义在消费侧」：端口定义在此（供 AgentService 等编排层消费），
// 适配器实现放在 internal/agent/repository 子包中。

// TaskRepository 持久化 Agent 任务。
type TaskRepository interface {
	Create(ctx context.Context, task *Task) error
	Get(ctx context.Context, id int64) (*Task, error)
	Update(ctx context.Context, task *Task) error
	ListByStatus(ctx context.Context, statuses ...TaskStatus) ([]*Task, error)
	// Page 分页查询任务（按 CreatedAt 升序）；statuses 为空表示全部状态。
	Page(ctx context.Context, offset, limit int, statuses ...TaskStatus) ([]*Task, int64, error)
}

// PermissionRepository 持久化权限请求。
type PermissionRepository interface {
	Create(ctx context.Context, permission *PermissionRequest) error
	Get(ctx context.Context, id int64) (*PermissionRequest, error)
	Update(ctx context.Context, permission *PermissionRequest) error
	ListPendingByTask(ctx context.Context, taskID int64) ([]*PermissionRequest, error)
	// Page 分页查询权限请求（按 CreatedAt 升序）；taskID 为 0 表示全部任务，statuses 为空表示全部状态。
	Page(ctx context.Context, offset, limit int, taskID int64, statuses ...PermissionStatus) ([]*PermissionRequest, int64, error)
}

// QuestionRepository 持久化用户提问（AskUser）。与 PermissionRepository 同构。
type QuestionRepository interface {
	Create(ctx context.Context, question *QuestionRequest) error
	Get(ctx context.Context, id int64) (*QuestionRequest, error)
	Update(ctx context.Context, question *QuestionRequest) error
	ListPendingByTask(ctx context.Context, taskID int64) ([]*QuestionRequest, error)
	// Page 分页查询提问请求（按 CreatedAt 升序）；taskID 为 0 表示全部任务，statuses 为空表示全部状态。
	Page(ctx context.Context, offset, limit int, taskID int64, statuses ...QuestionStatus) ([]*QuestionRequest, int64, error)
}
