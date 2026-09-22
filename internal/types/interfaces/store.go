package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

type StoreService interface {
	CreateStore(ctx context.Context, item *types.Store) error
	GetStoreByID(ctx context.Context, id int64) (*types.Store, error)
	GetStoreByStoreID(ctx context.Context, storeID string) (*types.Store, error)
	GetStoreByURL(ctx context.Context, rawURL string) (*types.Store, error)
	UpdateStore(ctx context.Context, item *types.Store) error
	// UpdateStoreURL 只更新 url 单列（发布到远程时写目标仓库地址）。
	UpdateStoreURL(ctx context.Context, id int64, rawURL string) error
	DeleteStore(ctx context.Context, id int64) error
	ListStore(ctx context.Context) ([]*types.Store, error)
	PageStore(ctx context.Context, userID string, pagination *types.Pagination, query *types.StorePageQuery) (*types.PageResult, error)
}

type StoreRepository interface {
	CreateStore(ctx context.Context, item *types.Store) error
	GetStoreByID(ctx context.Context, id int64) (*types.Store, error)
	GetStoreByStoreID(ctx context.Context, storeID string) (*types.Store, error)
	GetStoreByURL(ctx context.Context, rawURL string) (*types.Store, error)
	UpdateStore(ctx context.Context, item *types.Store) error
	// UpdateStoreURL 只更新 url 单列（发布到远程时写目标仓库地址）。
	UpdateStoreURL(ctx context.Context, id int64, rawURL string) error
	DeleteStore(ctx context.Context, id int64) error
	ListStore(ctx context.Context) ([]*types.Store, error)
	PageStore(ctx context.Context, pagination *types.Pagination, query *types.StorePageQuery) ([]*types.Store, int64, error)
	ListInstalledWorkflowMap(ctx context.Context, activeProjectID int64, storeIDs []int64) (map[int64]uint, error)
	ListInstalledScriptMap(ctx context.Context, activeProjectID int64, storeIDs []int64) (map[int64]int64, error)
}
