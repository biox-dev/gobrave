package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/types"
	"gorm.io/gorm"
)

// SessionService 提供会话的增删改查。
type SessionService interface {
	Create(ctx context.Context, params types.SessionCreateParams) (types.Session, error)
	// CreateTitleSession 为 parentSessionID 创建一个「生成标题」子会话。
	CreateTitleSession(ctx context.Context, parentSessionID int64) (types.Session, error)
	// CreateTaskSession 以 toolCallID 作为会话 ID 创建子任务会话。
	CreateTaskSession(ctx context.Context, toolCallID, parentSessionID int64, title string) (types.Session, error)
	// CreateChildSession 创建 ParentSessionID 指向父会话的子会话（主键由雪花 ID 生成）。
	CreateChildSession(ctx context.Context, params types.SessionChildParams) (types.Session, error)
	// Fork 把源会话的历史复制到一个新会话，返回新会话。
	Fork(ctx context.Context, params types.SessionForkParams) (types.Session, error)
	// ForkWithMessages 把源会话的消息经 create 映射后写入新会话（同一事务），返回新会话。
	// create 拿到新会话与事务句柄，负责按调用方规则写入消息行。
	ForkWithMessages(ctx context.Context, params types.SessionForkParams, create func(tx *gorm.DB, forked *types.Session) error) (types.Session, error)
	Get(ctx context.Context, id int64) (types.Session, error)
	List(ctx context.Context, params types.SessionListParams) ([]types.Session, int64, error)
	Save(ctx context.Context, session types.Session) (types.Session, error)
	// UpdateTitleIfUnchanged 仅当当前标题仍等于 priorTitle 时更新标题，返回是否命中。
	UpdateTitleIfUnchanged(ctx context.Context, id int64, priorTitle, title string) (bool, error)
	Delete(ctx context.Context, id int64) error
}

// SessionResourceService 记录并查询「会话 ↔ 资源」关联。
type SessionResourceService interface {
	// LinkFromContext 记录当前工具执行会话与资源的关联。
	// 非 Agent 会话触发的写操作（ctx 中没有会话 ID）会静默跳过，不视为错误。
	// 只有真正新增关联时才向前端推送一次 component.invoke（agent-session-resource），
	// 用于刷新会话资源面板；重复关联不推送。
	LinkFromContext(ctx context.Context, resourceType types.SessionResourceType, resourceIDs ...int64) error
	// List 返回某个会话关联的指定类型资源（按创建时间倒序）。
	List(ctx context.Context, sessionID int64, resourceType types.SessionResourceType) ([]types.SessionResource, error)
	// ListBySession 返回某个会话关联的全部类型资源（按创建时间倒序）。
	ListBySession(ctx context.Context, sessionID int64) ([]types.SessionResource, error)
	// Get 按关联行主键返回一条关联；不存在时返回 nil。
	Get(ctx context.Context, id int64) (*types.SessionResource, error)
	// Unlink 按关联行主键删除一条关联：只删除「会话 ↔ 资源」关联行，
	// 不删除脚本 / 工作流 / 分析等资源本身。关联不存在时视为成功（幂等）。
	Unlink(ctx context.Context, id int64) error
}

// PlanService 提供计划（AgentPlan，项目级、可跨会话复用的步骤树）的增删改查。
type PlanService interface {
	// Save 新建（ID=0）或整体覆盖计划，返回保存后的记录。
	Save(ctx context.Context, plan *types.AgentPlan) (*types.AgentPlan, error)
	Get(ctx context.Context, id int64) (*types.AgentPlan, error)
	ListByProject(ctx context.Context, projectID int64) ([]*types.AgentPlan, error)
	Delete(ctx context.Context, id int64) error
	// UpdateItemStatus 上报若干计划节点的执行状态，返回合并状态后的完整计划。
	//
	// 只写运行态（agent_plan_item_states），不改计划结构；调用方（工具）从执行事实
	// 取 SessionID。节点 ID 不存在时返回错误，避免 agent 写错目标。
	UpdateItemStatus(ctx context.Context, planID int64, sessionID int64, updates []types.PlanItemStatusUpdate) (*types.AgentPlan, error)
}

// MessageService 提供消息的增删改查。
type MessageService interface {
	Create(ctx context.Context, params types.MessageCreateParams) (types.Message, error)
	Update(ctx context.Context, message types.Message) (types.Message, error)
	Get(ctx context.Context, id int64) (types.Message, error)
	List(ctx context.Context, sessionID int64) ([]types.Message, error)
	// ListPage 分页查询某会话的消息（按 Seq 升序）；beforeSeq 为向上翻页游标，
	// 0 表示取最新一页；hasMore 表示还有更早的消息。
	ListPage(ctx context.Context, sessionID int64, beforeSeq, limit int) ([]types.Message, bool, error)
	// ListParticipants 返回在某会话中发过言（写过 user 消息）的用户 ID 与显示名（去重）。
	ListParticipants(ctx context.Context, sessionID int64) ([]types.SessionUser, error)
	// ListByTask 返回某任务的全部消息（含该轮的 user 消息），按 Seq 升序。
	ListByTask(ctx context.Context, taskID int64) ([]types.Message, error)
	Delete(ctx context.Context, id int64) error
	// DeleteByTask 删除某任务的全部消息（含该轮的 user 消息）。
	DeleteByTask(ctx context.Context, taskID int64) error
	DeleteSessionMessages(ctx context.Context, sessionID int64) error
}

// HistoryService 暴露文件历史的读写能力。
type HistoryService interface {
	// Create 记录文件的初始版本。
	Create(ctx context.Context, sessionID int64, path, content string) (*types.File, error)
	// CreateVersion 记录文件的新版本（版本号自动递增）。
	CreateVersion(ctx context.Context, sessionID int64, path, content string) (*types.File, error)
	Get(ctx context.Context, id int64) (*types.File, error)
	GetByPathAndSession(ctx context.Context, path string, sessionID int64) (*types.File, error)
	ListBySession(ctx context.Context, sessionID int64) ([]*types.File, error)
	ListLatestSessionFiles(ctx context.Context, sessionID int64) ([]*types.File, error)
	Update(ctx context.Context, file *types.File) (*types.File, error)
	Delete(ctx context.Context, id int64) error
	DeleteSessionFiles(ctx context.Context, sessionID int64) error
}
