package container

import (
	"errors"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
	"gorm.io/gorm"
)

const (
	legacyContainerImageTable        = "go_container_image"
	containerTemplateDefinitionTable = "go_container_template_definition"
	legacyContainerTemplateImageIdx  = "uk_template_spec_image"
)

// migrateLegacyContainerImages 把旧的 go_container_image 数据迁移到绑定行并删除旧结构。
//
// 旧模型里镜像单独建表，绑定行只有 image_id 引用；新模型把完整镜像名与拉取策略直接存在
// go_container_template_definition 上。AutoMigrate 会新增 image_name / pull_policy 两列，
// 这里负责：
//  1. 按 image_id 把 go_container_image.full_name / pull_policy 回填到绑定行；
//  2. 删除旧的 (spec_id, image_id) 唯一索引；
//  3. 删除旧列 image_id 与旧表 go_container_image。
//
// 迁移是幂等的：旧列/旧表不存在时直接跳过。
func migrateLegacyContainerImages(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(containerTemplateDefinitionTable) {
		return nil
	}

	hasImageID := db.Migrator().HasColumn(containerTemplateDefinitionTable, "image_id")
	if hasImageID && db.Migrator().HasTable(legacyContainerImageTable) {
		type legacyDefinition struct {
			ID      int64
			ImageID int64
		}
		type legacyImage struct {
			FullName   string
			PullPolicy string
		}

		definitions := make([]legacyDefinition, 0)
		if err := db.Table(containerTemplateDefinitionTable).
			Select("id, image_id").
			Where("image_id IS NOT NULL AND image_id <> 0").
			Scan(&definitions).Error; err != nil {
			return err
		}

		for _, definition := range definitions {
			image := legacyImage{}
			err := db.Table(legacyContainerImageTable).
				Select("full_name, pull_policy").
				Where("id = ?", definition.ImageID).
				Take(&image).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}

			updates := map[string]any{"image_name": image.FullName}
			if strings.TrimSpace(image.PullPolicy) != "" {
				updates["pull_policy"] = image.PullPolicy
			}
			if err := db.Table(containerTemplateDefinitionTable).
				Where("id = ?", definition.ID).
				Updates(updates).Error; err != nil {
				return err
			}
		}
	}

	if db.Migrator().HasIndex(containerTemplateDefinitionTable, legacyContainerTemplateImageIdx) {
		if err := db.Migrator().DropIndex(containerTemplateDefinitionTable, legacyContainerTemplateImageIdx); err != nil {
			return err
		}
	}

	if hasImageID {
		// DropColumn 需要 model（sqlite 实现会在 Schema 上查找字段），image_id 已不在
		// 新 model 中，LookUpField 返回 nil 时按列名删除。
		if err := db.Migrator().DropColumn(&types.ContainerTemplateDefinition{}, "image_id"); err != nil {
			return err
		}
	}

	if db.Migrator().HasTable(legacyContainerImageTable) {
		if err := db.Migrator().DropTable(legacyContainerImageTable); err != nil {
			return err
		}
	}

	return nil
}
