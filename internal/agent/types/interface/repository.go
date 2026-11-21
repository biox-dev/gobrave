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
	// UpdateTitleIfUnchanged 仅当当前标题仍等于 priorTitle 时更新标题，返回是否命中。
	//
	// 用一条原子 UPDATE 完成「比较 + 写入」：避免异步标题生成与用户改名 /
	// 轮次收尾（同样是整行 Save）并发时互相覆盖。
	UpdateTitleIfUnchanged(ctx context.Context, id int64, priorTitle, title string) (bool, error)
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context, params types.SessionListParams) ([]*types.Session, int64, error)
	// Fork 在一个事务内复制源会话的消息（可选复制文件历史最新版本）到新会话。
	//
	// 事务内完成「创建新会话 + 逐条复制消息 + 复制最新文件版本」，任一环节失败
	// 整体回滚，不会留下半成品会话。
	Fork(ctx context.Context, params types.SessionForkParams) (*types.Session, error)
}

// MessageStore 是 Message 的持久化端口。
type MessageStore interface {
	Create(ctx context.Context, m *types.Message) error
	Update(ctx context.Context, m *types.Message) error
	Get(ctx context.Context, id int64) (*types.Message, error)
	List(ctx context.Context, sessionID int64) ([]*types.Message, error)
	// ListPage 分页查询某会话的消息（按 Seq 升序返回）。
	//
	// beforeSeq > 0 时只返回 Seq 更小的消息（向上翻页），0 表示从最新消息开始。
	// hasMore 表示在本次返回的最早一条消息之前仍有消息。
	ListPage(ctx context.Context, sessionID int64, beforeSeq, limit int) ([]*types.Message, bool, error)
	// ListParticipants 返回在某会话中发过言（写过 user 消息）的用户 ID 与显示名（去重）。
	//
	// 只统计 RoleUser 且 UserID > 0 的消息（Agent 侧消息的 UserID 是 Profile ID）；
	// 用户名从 users 表回填，精简库（无 users 表）下 Username 为空但不影响 ID 返回。
	ListParticipants(ctx context.Context, sessionID int64) ([]types.SessionUser, error)
	Delete(ctx context.Context, id int64) error
	DeleteBySession(ctx context.Context, sessionID int64) error
}

// SessionResourceStore 是「会话 ↔ 资源」关联的持久化端口。
type SessionResourceStore interface {
	// Link 记录会话与资源（脚本 / 工作流 / 分析节点）的关联。
	// 同一 (session_id, resource_type, resource_id) 重复写入时静默跳过，保证关联不重复。
	// inserted 是本次真正新增的行数：0 表示全是已存在的关联（不算「有新数据」）。
	Link(ctx context.Context, sessionID int64, resourceType types.SessionResourceType, resourceIDs []int64) (inserted int64, err error)
	// List 返回某个会话关联的指定类型资源（按创建时间倒序）。
	List(ctx context.Context, sessionID int64, resourceType types.SessionResourceType) ([]*types.SessionResource, error)
	// ListBySession 返回某个会话关联的全部类型资源（按创建时间倒序）。
	ListBySession(ctx context.Context, sessionID int64) ([]*types.SessionResource, error)
	// DeleteBySession 删除某个会话的全部资源关联（会话删除时级联清理）。
	DeleteBySession(ctx context.Context, sessionID int64) error
}

// PlanStore 是计划（agent_plans）的持久化端口。
type PlanStore interface {
	Save(ctx context.Context, plan *types.AgentPlan) error
	Get(ctx context.Context, id int64) (*types.AgentPlan, error)
	ListByProject(ctx context.Context, projectID int64) ([]*types.AgentPlan, error)
	Delete(ctx context.Context, id int64) error
}

// PlanStateStore 是计划节点运行态（agent_plan_item_states）的持久化端口。
//
// 以 (plan_id, item_id) 为主键做 upsert：同一节点重复上报只保留最新一行。
type PlanStateStore interface {
	Upsert(ctx context.Context, states []*types.AgentPlanItemState) error
	// ListByPlan 返回计划下全部有状态记录的节点（稀疏：未出现的节点即 pending）。
	ListByPlan(ctx context.Context, planID int64) ([]*types.AgentPlanItemState, error)
	// DeleteByPlan 删除计划的全部运行态（计划删除时级联清理）。
	DeleteByPlan(ctx context.Context, planID int64) error
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
