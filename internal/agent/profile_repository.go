package agent

import "context"

// ProfileRepository 持久化用户自定义 AgentProfile。
//
// 与 TaskRepository / MemoryRepository 一致：内置 Profile 由代码定义（不落库），
// 仅用户自定义 Profile 通过该接口持久化。实现见 internal/agent/repository 子包。
type ProfileRepository interface {
	Create(ctx context.Context, profile *Profile) error
	Get(ctx context.Context, id int64) (*Profile, error)
	Update(ctx context.Context, profile *Profile) error
	Delete(ctx context.Context, id int64) error
	// ListByUser 返回某用户全部自定义 Profile（按名称升序）。
	ListByUser(ctx context.Context, userID int64) ([]*Profile, error)
	// ListBuiltin 返回全部内置 Profile（按名称升序）。
	ListBuiltin(ctx context.Context) ([]*Profile, error)
	// GetDefault 返回某用户的默认自定义 Profile；不存在时返回 (nil, nil)。
	GetDefault(ctx context.Context, userID int64) (*Profile, error)
	// ClearDefault 清除某用户除 exceptID 外的默认标记。
	ClearDefault(ctx context.Context, userID int64, exceptID int64) error
}
