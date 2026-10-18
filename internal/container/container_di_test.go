package container

import (
	"testing"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"go.uber.org/dig"
	"gorm.io/gorm"
)

// TestBuildContainerRegistersContainerService 回归测试：BuildContainer 里的
// 启动期 Invoke 必须在依赖注册之后执行。曾因「内置容器模板导入」的 Invoke 写在
// service.NewContainerService 之前，导致启动直接 panic：
//
//	panic: missing dependencies for function "BuildContainer.func21": missing type: interfaces.ContainerService
//
// 该测试用最小 sqlite 配置真正构建一次容器，确保所有启动期 Invoke 的依赖都能解析，
// 并校验内置容器模板（builtin/container_templates/*.json）确实在启动时被导入。
func TestBuildContainerRegistersContainerService(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Storage:  &config.StorageConfig{BaseDir: dir},
		Database: &config.DatabaseConfig{Driver: "sqlite", Path: dir + "/test.db"},
	}

	container := dig.New()
	// 出错即 panic（must 的行为）；这里 recovering 后转成测试失败信息，便于定位。
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("BuildContainer panicked: %v", r)
		}
	}()
	_ = BuildContainer(container, cfg)

	// 内置容器模板必须在启动时落库（内置 JSON 里 in_default=true 的 rstudio 模板）。
	if err := container.Invoke(func(db *gorm.DB) error {
		var images int64
		if err := db.Model(&types.ContainerImage{}).Count(&images).Error; err != nil {
			return err
		}
		if images != 2 {
			t.Fatalf("container images = %d, want 2 (builtin templates not installed)", images)
		}

		var defaults int64
		if err := db.Model(&types.ContainerTemplateDefinition{}).
			Where("is_default = ?", true).Count(&defaults).Error; err != nil {
			return err
		}
		if defaults != 1 {
			t.Fatalf("default template definitions = %d, want 1", defaults)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect db: %v", err)
	}
}
