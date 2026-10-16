package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	apperrors "github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// snowflakeOnce 保证测试进程内只初始化一次 snowflake 节点（GenerateID 依赖它）。
var snowflakeOnce sync.Once

func ensureSnowflake(t *testing.T) {
	t.Helper()
	snowflakeOnce.Do(func() {
		if err := utils.InitSnowflake(1); err != nil {
			t.Fatalf("init snowflake: %v", err)
		}
	})
}

// stubProjectRepo 只为「报告条目重复校验」测试提供最小实现，其余方法沿用接口零值。
type stubProjectRepo struct {
	interfaces.ProjectRepository
	report    *types.ProjectReport
	ownerSeen bool
	added     []*types.ProjectReportItem
	ownerHit  []types.ProjectReportItemOwnerType
	// items 模拟仓储层按全局 sort_order 返回的原始条目列表。
	items []*types.ProjectReportItem
}

func (r *stubProjectRepo) GetProjectReportByID(_ context.Context, reportID int64) (*types.ProjectReport, error) {
	if r.report == nil || r.report.ID != reportID {
		return nil, apperrors.NewNotFoundError("project report not found")
	}
	return r.report, nil
}

func (r *stubProjectRepo) ExistsUserProject(_ context.Context, _ int64, _ int64) (bool, error) {
	return true, nil
}

func (r *stubProjectRepo) ExistsProjectReportItemOwner(
	_ context.Context,
	_ int64,
	ownerType types.ProjectReportItemOwnerType,
	_ int64,
	_ int64,
) (bool, error) {
	r.ownerHit = append(r.ownerHit, ownerType)
	return r.ownerSeen, nil
}

func (r *stubProjectRepo) AddProjectReportItem(_ context.Context, item *types.ProjectReportItem) error {
	r.added = append(r.added, item)
	return nil
}

func (r *stubProjectRepo) ListProjectReportItemsByReportID(_ context.Context, reportID int64) ([]*types.ProjectReportItem, error) {
	if r.report == nil || r.report.ID != reportID {
		return nil, apperrors.NewNotFoundError("project report not found")
	}
	return r.items, nil
}

func newProjectReportItemTestService(repo *stubProjectRepo) *projectService {
	return &projectService{projectRepo: repo}
}

// TestAddProjectReportItemRejectsDuplicateOwner 确认同一报告下相同 owner_type + owner_id 会被拒绝（409）。
func TestAddProjectReportItemRejectsDuplicateOwner(t *testing.T) {
	ensureSnowflake(t)
	repo := &stubProjectRepo{
		report:    &types.ProjectReport{ID: 42, ProjectID: 7},
		ownerSeen: true,
	}
	svc := newProjectReportItemTestService(repo)

	err := svc.AddProjectReportItem(context.Background(), 1, &types.ProjectReportItem{
		ProjectReportID: 42,
		OwnerType:       types.ProjectReportItemOwnerAnalysis,
		OwnerID:         99,
	})
	if err == nil {
		t.Fatal("expected duplicate owner to be rejected")
	}

	var appErr *apperrors.AppError
	if !errors.As(err, &appErr) || appErr.Code != apperrors.ErrConflict {
		t.Fatalf("expected conflict app error, got %v", err)
	}
	if len(repo.added) != 0 {
		t.Fatalf("duplicate item must not be persisted, got %d rows", len(repo.added))
	}
}

// TestAddProjectReportItemAllowsUniqueOwner 确认未重复的 owner 引用可以正常写入。
func TestAddProjectReportItemAllowsUniqueOwner(t *testing.T) {
	ensureSnowflake(t)
	repo := &stubProjectRepo{report: &types.ProjectReport{ID: 42, ProjectID: 7}}
	svc := newProjectReportItemTestService(repo)

	err := svc.AddProjectReportItem(context.Background(), 1, &types.ProjectReportItem{
		ProjectReportID: 42,
		OwnerType:       types.ProjectReportItemOwnerAnalysisNode,
		OwnerID:         100,
		Title:           "should be cleared",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.added) != 1 {
		t.Fatalf("expected 1 persisted item, got %d", len(repo.added))
	}
	if repo.added[0].Title != "" {
		t.Fatalf("referenced item title must be cleared, got %q", repo.added[0].Title)
	}
}

// TestAddProjectReportItemCustomNotDeduplicated 确认 custom 条目（无 owner 引用）不受判重限制，且会被重置 OwnerID。
func TestAddProjectReportItemCustomNotDeduplicated(t *testing.T) {
	ensureSnowflake(t)
	repo := &stubProjectRepo{
		report:    &types.ProjectReport{ID: 42, ProjectID: 7},
		ownerSeen: true,
	}
	svc := newProjectReportItemTestService(repo)

	item := &types.ProjectReportItem{
		ProjectReportID: 42,
		OwnerType:       types.ProjectReportItemOwnerCustom,
		OwnerID:         123,
		Title:           "Section",
		Content:         "text",
	}
	if err := svc.AddProjectReportItem(context.Background(), 1, item); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.ownerHit) != 0 {
		t.Fatalf("custom item must skip duplicate check, got %v", repo.ownerHit)
	}
	if len(repo.added) != 1 {
		t.Fatalf("expected 1 persisted item, got %d", len(repo.added))
	}
	if item.OwnerID != 0 {
		t.Fatalf("custom item owner_id must be reset to 0, got %d", item.OwnerID)
	}
	if repo.added[0].Title != "Section" || repo.added[0].Content != "text" {
		t.Fatalf("custom item title/content must be persisted, got %q/%q", repo.added[0].Title, repo.added[0].Content)
	}
}

// TestListProjectReportItemsByReportIDUsesTreeOrder 确认服务层把仓储的「全局 sort_order」原始列表
// 重排为树的前序遍历（父节点紧跟其子节点），与前端 tree 的展开顺序一致。
func TestListProjectReportItemsByReportIDUsesTreeOrder(t *testing.T) {
	repo := &stubProjectRepo{
		report: &types.ProjectReport{ID: 42, ProjectID: 7},
		// 模拟仓储按 sort_order 升序返回：子节点(0) 排在父节点(5) 之前，与树序不同。
		items: []*types.ProjectReportItem{
			{ID: 20, ProjectReportID: 42, ParentID: 10, SortOrder: 0, OwnerType: types.ProjectReportItemOwnerCustom, Title: "R2 child"},
			{ID: 11, ProjectReportID: 42, SortOrder: 0, OwnerType: types.ProjectReportItemOwnerCustom, Title: "R1"},
			{ID: 10, ProjectReportID: 42, SortOrder: 5, OwnerType: types.ProjectReportItemOwnerCustom, Title: "R2"},
		},
	}
	svc := newProjectReportItemTestService(repo)

	items, err := svc.ListProjectReportItemsByReportID(context.Background(), 1, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := make([]int64, 0, len(items))
	for _, item := range items {
		got = append(got, item.ID)
	}

	want := []int64{11, 10, 20}
	if len(got) != len(want) {
		t.Fatalf("expected %d items in tree order, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected tree order %v, got %v", want, got)
		}
	}
}
