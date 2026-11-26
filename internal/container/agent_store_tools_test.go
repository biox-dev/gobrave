package container

import (
	"context"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/datatypes"
)

// fakeStoreService 是 storeTools 测试用的最小 StoreService 实现。
// 内嵌 interfaces.StoreService 后只需实现被用到的方法。
type fakeStoreService struct {
	interfaces.StoreService

	pageResult *types.PageResult
	pageErr    error
	gotUserID  int64
	gotQuery   *types.StorePageQuery

	stores map[int64]*types.Store
}

func (f *fakeStoreService) PageStore(_ context.Context, userID int64, _ *types.Pagination, query *types.StorePageQuery) (*types.PageResult, error) {
	f.gotUserID = userID
	f.gotQuery = query
	if f.pageErr != nil {
		return nil, f.pageErr
	}
	return f.pageResult, nil
}

func (f *fakeStoreService) GetStoreByID(_ context.Context, id int64) (*types.Store, error) {
	if f.stores == nil {
		return nil, errFakeNotFound
	}
	item, ok := f.stores[id]
	if !ok {
		return nil, errFakeNotFound
	}
	cloned := *item
	return &cloned, nil
}

func newStoreToolsForTest(storeSvc interfaces.StoreService) *storeTools {
	return newStoreTools(storeSvc, nil, nil, nil, &config.Config{
		Storage: &config.StorageConfig{BaseDir: "/data"},
	})
}

func TestStoreToolsPageStoreMapsItems(t *testing.T) {
	svc := &fakeStoreService{pageResult: types.NewPageResult(1, &types.Pagination{Page: 1, PageSize: 20}, []*types.StoreDTO{
		{
			Store: types.Store{
				ID:        11,
				StoreType: "workflow",
				Name:      "qc",
				Origin:    "local",
				PathName:  "abc",
				Category:  "qc",
				Tags:      datatypes.JSON([]byte(`["a","b"]`)),
			},
			Installed:   true,
			InstalledID: "5",
		},
	})}

	out, err := newStoreToolsForTest(svc).PageStore(context.Background(), 9, runtimecontext.StorePageInput{
		StoreType: " WORKFLOW ",
		Keywords:  "  qc ",
	})
	if err != nil {
		t.Fatalf("page store: %v", err)
	}
	if svc.gotUserID != 9 {
		t.Fatalf("user id = %d, want 9", svc.gotUserID)
	}
	if svc.gotQuery == nil || svc.gotQuery.StoreType != "workflow" || svc.gotQuery.Keywords != "qc" {
		t.Fatalf("query = %+v", svc.gotQuery)
	}
	if out.Total != 1 || len(out.Stores) != 1 {
		t.Fatalf("output = %+v", out)
	}
	item := out.Stores[0]
	if item.StoreID != 11 || item.StorePath != "/data/store/abc" || !item.Installed || item.InstalledID != "5" {
		t.Fatalf("item = %+v", item)
	}
	if len(item.Tags) != 2 || item.Tags[0] != "a" || item.Tags[1] != "b" {
		t.Fatalf("tags = %+v", item.Tags)
	}
}

func TestStoreToolsPageStoreRejectsUnknownType(t *testing.T) {
	svc := &fakeStoreService{}
	if _, err := newStoreToolsForTest(svc).PageStore(context.Background(), 9, runtimecontext.StorePageInput{StoreType: "docker"}); err == nil {
		t.Fatalf("expected store_type validation error")
	}
}

func TestStoreToolsPageStoreRequiresUser(t *testing.T) {
	svc := &fakeStoreService{}
	if _, err := newStoreToolsForTest(svc).PageStore(context.Background(), 0, runtimecontext.StorePageInput{StoreType: "script"}); err == nil {
		t.Fatalf("expected user_id required error")
	}
}

func TestResolveStorePathNameForPublish(t *testing.T) {
	svc := &fakeStoreService{stores: map[int64]*types.Store{
		5: {ID: 5, StoreType: "workflow", PathName: "keep-me"},
	}}
	tools := newStoreToolsForTest(svc)

	existing, pathName, err := tools.resolveStorePathNameForPublish(context.Background(), 5)
	if err != nil {
		t.Fatalf("existing store: %v", err)
	}
	if existing == nil || existing.ID != 5 || pathName != "keep-me" {
		t.Fatalf("existing = %+v, pathName = %q", existing, pathName)
	}

	none, generated, err := tools.resolveStorePathNameForPublish(context.Background(), 0)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if none != nil || len(generated) != 32 {
		t.Fatalf("none = %+v, generated = %q", none, generated)
	}
}

func TestDecodeStoreTagsFallsBackToSprint(t *testing.T) {
	tags := decodeStoreTags(datatypes.JSON([]byte(`["a",1]`)))
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "1" {
		t.Fatalf("tags = %+v", tags)
	}
	if got := decodeStoreTags(nil); got != nil {
		t.Fatalf("nil tags = %+v", got)
	}
}
