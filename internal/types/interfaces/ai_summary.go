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
	UpdateAISummary(ctx context.Context, item *types.AISummary) error
	DeleteAISummary(ctx context.Context, id int64) error
}

// AISummaryService 定义 AI 摘要的业务操作。
type AISummaryService interface {
	// CreateAISummary 创建摘要记录并投递异步生成事件。
	CreateAISummary(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) (*types.AISummary, error)
	// RegenerateAISummary 按摘要 ID 重新投递异步生成事件。
	RegenerateAISummary(ctx context.Context, id int64) (*types.AISummary, error)
	GetAISummaryByID(ctx context.Context, id int64) (*types.AISummary, error)
	// ListAISummariesByOwner 按所属对象类型与 ID 查询摘要列表。
	ListAISummariesByOwner(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) ([]*types.AISummary, error)
	// DeleteAISummary 按摘要 ID 删除摘要记录。
	DeleteAISummary(ctx context.Context, id int64) error
	// GetAISummaryInput 按所属对象类型与 ID 解析生成摘要时交给 LLM 的输入信息。
	GetAISummaryInput(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) (*types.AISummaryInput, error)
}
