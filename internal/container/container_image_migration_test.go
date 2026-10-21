package container

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
)

// legacyContainerImage / legacyContainerTemplateDefinition 复刻旧版本的表结构，
// 保证测试里的旧库与真实历史库一致（由 GORM AutoMigrate 建表）。
type legacyContainerImage struct {
	ID         int64  `gorm:"primaryKey;type:bigint;autoIncrement:false"`
	Name       string `gorm:"type:varchar(255);not null;index"`
	FullName   string `gorm:"type:varchar(512);not null"`
	PullPolicy string `gorm:"type:varchar(32);index;not null;default:IfNotPresent"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (legacyContainerImage) TableName() string { return "go_container_image" }

type legacyContainerTemplateDefinition struct {
	ID          int64  `gorm:"primaryKey;type:bigint;autoIncrement:false"`
	SpecID      int64  `gorm:"uniqueIndex:uk_template_spec_image,priority:1;not null"`
	ImageID     int64  `gorm:"uniqueIndex:uk_template_spec_image,priority:2;not null"`
	DisplayName string `gorm:"type:varchar(255)"`
	IsDefault   bool   `gorm:"not null;default:false;index"`
	CreatedAt   *time.Time
	UpdatedAt   *time.Time
}

func (legacyContainerTemplateDefinition) TableName() string {
	return "go_container_template_definition"
}

// TestMigrateLegacyContainerImages 覆盖旧表 go_container_image 的数据回填与结构删除：
// 绑定行 image_id 指向的镜像，其 full_name / pull_policy 应被搬到 image_name / pull_policy，
// 随后旧列 image_id 与旧表 go_container_image 必须消失。
func TestMigrateLegacyContainerImages(t *testing.T) {
	db, err := gorm.Open(gormlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	if err := db.AutoMigrate(&legacyContainerImage{}, &legacyContainerTemplateDefinition{}); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := db.Create(&legacyContainerImage{ID: 100, Name: "rocker/rstudio", FullName: "docker.io/rocker/rstudio:4.4", PullPolicy: types.PullPolicyAlways}).Error; err != nil {
		t.Fatalf("seed image: %v", err)
	}
	if err := db.Create(&legacyContainerTemplateDefinition{ID: 200, SpecID: 300, ImageID: 100, DisplayName: "RStudio"}).Error; err != nil {
		t.Fatalf("seed definition: %v", err)
	}

	// AutoMigrate 会新增 image_name / pull_policy（与真实启动顺序一致）。
	if err := db.AutoMigrate(&types.ContainerTemplateDefinition{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	if err := migrateLegacyContainerImages(db); err != nil {
		t.Fatalf("migrateLegacyContainerImages: %v", err)
	}

	var row struct {
		ImageName  string
		PullPolicy string
	}
	if err := db.Table(containerTemplateDefinitionTable).
		Select("image_name, pull_policy").
		Where("id = ?", 200).
		Scan(&row).Error; err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if row.ImageName != "docker.io/rocker/rstudio:4.4" || row.PullPolicy != types.PullPolicyAlways {
		t.Fatalf("migrated row = %#v", row)
	}

	if db.Migrator().HasTable(legacyContainerImageTable) {
		t.Fatal("legacy image table should be dropped")
	}
	if db.Migrator().HasColumn(containerTemplateDefinitionTable, "image_id") {
		t.Fatal("legacy image_id column should be dropped")
	}

	// 幂等：再跑一次不应报错。
	if err := migrateLegacyContainerImages(db); err != nil {
		t.Fatalf("second migrateLegacyContainerImages: %v", err)
	}
}
