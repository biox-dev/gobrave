// Package interfaces 定义 Agent 领域的 Repository / Service 端口。
//
// 端口与实现分离：本包只依赖 types，具体实现在
// application/repository（持久化）与 application/service（编排）。
package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/types"
)

// TaskRepository 持久化 Agent 任务。
type TaskRepository interface {
	Create(ctx context.Context, task *types.Task) error
	Get(ctx context.Context, id int64) (*types.Task, error)
	Update(ctx context.Context, task *types.Task) error
	// UpdateResumeState 只更新可恢复执行检查点，避免与状态迁移并发时互相覆盖。
	// state 为 nil 表示清理检查点。
	UpdateResumeState(ctx context.Context, id int64, state *types.TaskResumeState) error
	ListByStatus(ctx context.Context, statuses ...types.TaskStatus) ([]*types.Task, error)
	// Page 分页查询任务（按 CreatedAt 升序）；statuses 为空表示全部状态。
	Page(ctx context.Context, offset, limit int, statuses ...types.TaskStatus) ([]*types.Task, int64, error)
}

// PermissionRepository 持久化权限请求。
type PermissionRepository interface {
	Create(ctx context.Context, permission *types.PermissionRequest) error
	Get(ctx context.Context, id int64) (*types.PermissionRequest, error)
	Update(ctx context.Context, permission *types.PermissionRequest) error
	ListPendingByTask(ctx context.Context, taskID int64) ([]*types.PermissionRequest, error)
	// ListByTask 返回某任务的全部权限请求（按创建时间倒序）。
	// 恢复执行时据此把已批准的权限与待执行的工具调用精确配对。
	ListByTask(ctx context.Context, taskID int64) ([]*types.PermissionRequest, error)
	// Page 分页查询权限请求（按 CreatedAt 升序）；taskID 为 0 表示全部任务，statuses 为空表示全部状态。
	Page(ctx context.Context, offset, limit int, taskID int64, statuses ...types.PermissionStatus) ([]*types.PermissionRequest, int64, error)
}

// QuestionRepository 持久化用户提问（AskUser）。与 PermissionRepository 同构。
type QuestionRepository interface {
	Create(ctx context.Context, question *types.QuestionRequest) error
	Get(ctx context.Context, id int64) (*types.QuestionRequest, error)
	Update(ctx context.Context, question *types.QuestionRequest) error
	ListPendingByTask(ctx context.Context, taskID int64) ([]*types.QuestionRequest, error)
	// ListByTask 返回某任务的全部提问请求（按创建时间倒序）。
	ListByTask(ctx context.Context, taskID int64) ([]*types.QuestionRequest, error)
	// Page 分页查询提问请求（按 CreatedAt 升序）；taskID 为 0 表示全部任务，statuses 为空表示全部状态。
	Page(ctx context.Context, offset, limit int, taskID int64, statuses ...types.QuestionStatus) ([]*types.QuestionRequest, int64, error)
}

// MemoryRepository 持久化记忆。
type MemoryRepository interface {
	Create(ctx context.Context, memory *types.Memory) error
	Get(ctx context.Context, id int64) (*types.Memory, error)
	Update(ctx context.Context, memory *types.Memory) error
	Delete(ctx context.Context, id int64) error
	// ListByUser 分页查询某用户的记忆（按 UpdatedAt 降序）；kinds 为空表示全部类别。
	ListByUser(ctx context.Context, userID int64, offset, limit int, kinds ...types.MemoryKind) ([]*types.Memory, int64, error)
	// ListByUserContext 分页查询某用户的记忆，并优先返回 projectID / sessionID 匹配的记录。
	// projectID <= 0 / sessionID <= 0 时退化为普通的分页查询。
	ListByUserContext(ctx context.Context, userID int64, projectID int64, sessionID int64, offset, limit int, kinds ...types.MemoryKind) ([]*types.Memory, int64, error)
	// Search 按关键词检索某用户的记忆（返回按相关度 + 重要度排序）。
	// 基础实现为子串 / LIKE 匹配，可被更高级的检索器（向量 / 语义）替换。
	Search(ctx context.Context, userID int64, query string, limit int) ([]*types.Memory, error)
}

// ProfileRepository 持久化 AgentProfile（内置 Profile 与自定义 Profile 统一存储）。
type ProfileRepository interface {
	Create(ctx context.Context, profile *types.Profile) error
	Get(ctx context.Context, id int64) (*types.Profile, error)
	Update(ctx context.Context, profile *types.Profile) error
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context) ([]*types.Profile, error)
	// ClearDefault 清除除 exceptID 以外所有 Profile 的默认标记。
	ClearDefault(ctx context.Context, exceptID int64) error
}

// SessionStore 是 Session 的持久化端口。
type SessionStore interface {
	Create(ctx context.Context, s *types.Session) error
	Get(ctx context.Context, id int64) (*types.Session, error)
	Update(ctx context.Context, s *types.Session) error
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context, params types.SessionListParams) ([]*types.Session, int64, error)
}

// MessageStore 是 Message 的持久化端口。
type MessageStore interface {
	Create(ctx context.Context, m *types.Message) error
	Update(ctx context.Context, m *types.Message) error
	Get(ctx context.Context, id int64) (*types.Message, error)
	List(ctx context.Context, sessionID int64) ([]*types.Message, error)
	Delete(ctx context.Context, id int64) error
	DeleteBySession(ctx context.Context, sessionID int64) error
}

// HistoryStore 是文件历史的持久化端口。
type HistoryStore interface {
	Create(ctx context.Context, file *types.File) error
	Update(ctx context.Context, file *types.File) error
	Get(ctx context.Context, id int64) (*types.File, error)
	GetByPathAndSession(ctx context.Context, path string, sessionID int64) (*types.File, error)
	ListBySession(ctx context.Context, sessionID int64) ([]*types.File, error)
	ListByPathAndSession(ctx context.Context, path string, sessionID int64) ([]*types.File, error)
	ListLatestBySession(ctx context.Context, sessionID int64) ([]*types.File, error)
	Delete(ctx context.Context, id int64) error
	DeleteBySession(ctx context.Context, sessionID int64) error
}
