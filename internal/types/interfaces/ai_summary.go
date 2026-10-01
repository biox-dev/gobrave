package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

// AISummaryRepository 定义 AI 摘要的持久化操作。
type AISummaryRepository interface {
	CreateAISummary(ctx context.Context, item *types.AISummary) error
	GetAISummaryByID(ctx context.Context, id int64) (*types.AISummary, error)
	// ListAISummariesByOwner 按所属对象类型与 ID 查询摘要列表。
	ListAISummariesByOwner(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) ([]*types.AISummary, error)
	// PageAISummariesByProjectID 按项目 ID 分页查询摘要列表（不返回 content）。
	PageAISummariesByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64) ([]*types.AISummary, int64, error)
	UpdateAISummary(ctx context.Context, item *types.AISummary) error
	DeleteAISummary(ctx context.Context, id int64) error
}

// AISummaryService 定义 AI 摘要的业务操作。
type AISummaryService interface {
	// CreateAISummary 创建摘要记录并投递异步生成事件。
	// profile 为本次生成使用的 Agent Profile 名称，为空表示使用内置 summary Profile。
	// 摘要所属项目 ID 由生成阶段从所属对象（Analysis / AnalysisNode）解析并回填。
	CreateAISummary(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64, profile string) (*types.AISummary, error)
	// RegenerateAISummary 按摘要 ID 重新投递异步生成事件。
	RegenerateAISummary(ctx context.Context, id int64) (*types.AISummary, error)
	// GetAISummaryByID 按摘要 ID 查询详情，并填充 Prefix
	// （所属对象输出目录对应的 URL 前缀，供前端渲染 content 中的相对链接）。
	GetAISummaryByID(ctx context.Context, id int64) (*types.AISummary, error)
	// ListAISummariesByOwner 按所属对象类型与 ID 查询摘要列表，
	// 并填充每个摘要的 Prefix（所属对象输出目录对应的 URL 前缀）。
	ListAISummariesByOwner(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) ([]*types.AISummary, error)
	// PageAISummariesByProjectID 按项目 ID 分页查询摘要列表，不返回 content，
	// 也不解析 Prefix（前者由详情接口按 ID 返回，后者仅在渲染正文时需要）。
	PageAISummariesByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64) ([]*types.AISummary, int64, error)
	// UpdateAISummary 按摘要 ID 更新标题、内容与 Agent Profile（nil 表示不修改对应字段）。
	UpdateAISummary(ctx context.Context, id int64, title, content, profile *string) (*types.AISummary, error)
	// DeleteAISummary 按摘要 ID 删除摘要记录。
	DeleteAISummary(ctx context.Context, id int64) error
	// GetAISummaryInput 按所属对象类型与 ID 解析生成摘要时交给 LLM 的输入信息。
	GetAISummaryInput(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) (*types.AISummaryInput, error)
}
