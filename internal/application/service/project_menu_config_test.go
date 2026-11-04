package service

import (
	"context"
	"errors"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// stubActiveUserProjectRepo 只实现「按激活项目读写菜单配置」相关方法，其余沿用接口零值。
type stubActiveUserProjectRepo struct {
	interfaces.ProjectRepository
	up          *types.UserProject
	updatedFor  int64
	updatedConf *types.UserMenuConfig
}

func (r *stubActiveUserProjectRepo) GetActiveUserProjectByUserID(_ context.Context, _ int64) (*types.UserProject, error) {
	if r.up == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return r.up, nil
}

func (r *stubActiveUserProjectRepo) UpdateUserProjectMenuConfig(_ context.Context, _ int64, projectID int64, config types.UserMenuConfig) error {
	r.updatedFor = projectID
	r.updatedConf = &config
	return nil
}

// TestGetActiveUserMenuConfigReturnsProjectConfig 确认 /auth/me 读到的是激活项目的配置。
func TestGetActiveUserMenuConfigReturnsProjectConfig(t *testing.T) {
	repo := &stubActiveUserProjectRepo{up: &types.UserProject{
		ProjectID:  9,
		MenuConfig: types.UserMenuConfig{Tabs: []types.UserMenuTab{{URL: "/c/scripts/1"}}},
	}}
	svc := &projectService{projectRepo: repo}

	cfg, err := svc.GetActiveUserMenuConfig(context.Background(), 1)
	if err != nil {
		t.Fatalf("get active user menu config: %v", err)
	}
	if len(cfg.Tabs) != 1 || cfg.Tabs[0].URL != "/c/scripts/1" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

// TestGetActiveUserMenuConfigWithoutActiveProject 确认没有激活项目时返回空配置而不是错误。
func TestGetActiveUserMenuConfigWithoutActiveProject(t *testing.T) {
	svc := &projectService{projectRepo: &stubActiveUserProjectRepo{}}

	cfg, err := svc.GetActiveUserMenuConfig(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Tabs) != 0 || cfg.Bottom != nil {
		t.Fatalf("expected empty config, got %+v", cfg)
	}
}

// TestUpdateActiveUserMenuConfigTargetsActiveProject 确认写入的是激活项目的绑定行。
func TestUpdateActiveUserMenuConfigTargetsActiveProject(t *testing.T) {
	repo := &stubActiveUserProjectRepo{up: &types.UserProject{ProjectID: 9}}
	svc := &projectService{projectRepo: repo}

	next := types.UserMenuConfig{Tabs: []types.UserMenuTab{{URL: "/new"}}}
	if err := svc.UpdateActiveUserMenuConfig(context.Background(), 1, next); err != nil {
		t.Fatalf("update active user menu config: %v", err)
	}
	if repo.updatedFor != 9 || repo.updatedConf == nil || repo.updatedConf.Tabs[0].URL != "/new" {
		t.Fatalf("unexpected update: project=%d config=%+v", repo.updatedFor, repo.updatedConf)
	}
}

// TestUpdateActiveUserMenuConfigWithoutActiveProject 确认没有激活项目时透传 not found。
func TestUpdateActiveUserMenuConfigWithoutActiveProject(t *testing.T) {
	svc := &projectService{projectRepo: &stubActiveUserProjectRepo{}}

	err := svc.UpdateActiveUserMenuConfig(context.Background(), 1, types.UserMenuConfig{})
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected gorm.ErrRecordNotFound, got %v", err)
	}
}
