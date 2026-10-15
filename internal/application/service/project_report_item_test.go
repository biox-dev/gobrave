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
