package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/types"
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
	Get(ctx context.Context, id int64) (types.Session, error)
	List(ctx context.Context, params types.SessionListParams) ([]types.Session, int64, error)
	Save(ctx context.Context, session types.Session) (types.Session, error)
	Delete(ctx context.Context, id int64) error
}

// MessageService 提供消息的增删改查。
type MessageService interface {
	Create(ctx context.Context, params types.MessageCreateParams) (types.Message, error)
	Update(ctx context.Context, message types.Message) (types.Message, error)
	Get(ctx context.Context, id int64) (types.Message, error)
	List(ctx context.Context, sessionID int64) ([]types.Message, error)
	Delete(ctx context.Context, id int64) error
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
