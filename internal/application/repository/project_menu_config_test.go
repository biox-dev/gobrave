package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
)

// newUserProjectTestDB 打开共享内存 sqlite 并迁移 user_project，
// 用于校验菜单配置（serializer:json）的写入与按激活项目读取。
func newUserProjectTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(gormlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&types.UserProject{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// TestUserProjectMenuConfigRoundTrip 确认菜单配置能随 user_project 落库、按激活项目读回，
// 并且 UpdateUserProjectMenuConfig 的整份覆盖会经过 JSON serializer。
func TestUserProjectMenuConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(newUserProjectTestDB(t))

	active := &types.UserProject{
		UserID:    1,
		ProjectID: 2,
		IsActive:  true,
		CreatedAt: time.Now(),
		MenuConfig: types.UserMenuConfig{
			Bottom: &types.UserBottomPanel{
				Open:   true,
				Active: "terminal:1",
				Height: 317,
				Tabs:   []types.UserBottomTab{{Key: "terminal:1", Kind: "terminal", Title: "t"}},
			},
		},
	}
	if err := repo.AddUserProject(ctx, active); err != nil {
		t.Fatalf("add user project: %v", err)
	}
	// 同一用户还有一条非激活绑定，按激活项目读取时不能取到它。
	if err := repo.AddUserProject(ctx, &types.UserProject{
		UserID:     1,
		ProjectID:  3,
		IsActive:   false,
		CreatedAt:  time.Now(),
		MenuConfig: types.UserMenuConfig{Bottom: &types.UserBottomPanel{Active: "terminal:inactive"}},
	}); err != nil {
		t.Fatalf("add inactive user project: %v", err)
	}

	got, err := repo.GetActiveUserProjectByUserID(ctx, 1)
	if err != nil {
		t.Fatalf("get active user project: %v", err)
	}
	if got.ProjectID != 2 || got.MenuConfig.Bottom == nil || got.MenuConfig.Bottom.Active != "terminal:1" {
		t.Fatalf("unexpected active binding: %+v", got)
	}
	if got.MenuConfig.Bottom.Height != 317 || len(got.MenuConfig.Bottom.Tabs) != 1 {
		t.Fatalf("bottom panel not persisted: %+v", got.MenuConfig.Bottom)
	}

	next := types.UserMenuConfig{Bottom: &types.UserBottomPanel{Open: true, Active: "terminal:2", Height: 240}}
	if err := repo.UpdateUserProjectMenuConfig(ctx, 1, 2, next); err != nil {
		t.Fatalf("update menu config: %v", err)
	}
	reloaded, err := repo.GetUserProject(ctx, 1, 2)
	if err != nil {
		t.Fatalf("reload user project: %v", err)
	}
	if reloaded.MenuConfig.Bottom == nil || reloaded.MenuConfig.Bottom.Active != "terminal:2" {
		t.Fatalf("menu config not overwritten: %+v", reloaded.MenuConfig)
	}
}
