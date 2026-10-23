package agent

import "context"

// ProfileRepository 持久化 AgentProfile（内置 Profile 与自定义 Profile 统一存储）。
//
// Profile 不再按用户隔离：所有 Profile 全局可见，读取 / 引用一律按 ID 进行。
// 实现见 internal/agent/repository 子包。
type ProfileRepository interface {
	Create(ctx context.Context, profile *Profile) error
	Get(ctx context.Context, id int64) (*Profile, error)
	Update(ctx context.Context, profile *Profile) error
	Delete(ctx context.Context, id int64) error
	// List 返回全部 Profile（按名称升序）。
	List(ctx context.Context) ([]*Profile, error)
	// ClearDefault 清除除 exceptID 外的默认标记。
	ClearDefault(ctx context.Context, exceptID int64) error
}
