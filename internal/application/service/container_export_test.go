package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// stubExportRepo 只实现导出/导入用到的那几个仓储方法；其余方法由 nil 的结构体字段承担，
// 一旦被测代码走到未实现的方法会直接 panic，便于发现意外的依赖。
type stubExportRepo struct {
	interfaces.ContainerRepository
	specs       map[int64]*types.ContainerTemplateSpec
	definitions map[int64]*types.ContainerTemplateDefinition
	nextID      int64
}

func (r *stubExportRepo) next() int64 {
	r.nextID++
	return r.nextID
}

func (r *stubExportRepo) GetContainerTemplateSpecByID(_ context.Context, id int64) (*types.ContainerTemplateSpec, error) {
	item, ok := r.specs[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	clone := *item
	return &clone, nil
}

func (r *stubExportRepo) CreateContainerTemplateSpec(_ context.Context, item *types.ContainerTemplateSpec) error {
	if item.ID == 0 {
		item.ID = r.next()
	}
	clone := *item
	r.specs[item.ID] = &clone
	return nil
}

func (r *stubExportRepo) UpdateContainerTemplateSpec(_ context.Context, item *types.ContainerTemplateSpec) error {
	clone := *item
	r.specs[item.ID] = &clone
	return nil
}

func (r *stubExportRepo) GetContainerTemplateDefinitionByID(_ context.Context, id int64) (*types.ContainerTemplateDefinition, error) {
	item, ok := r.definitions[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	clone := *item
	return &clone, nil
}

func (r *stubExportRepo) CreateContainerTemplateDefinition(_ context.Context, item *types.ContainerTemplateDefinition) error {
	if item.ID == 0 {
		item.ID = r.next()
	}
	clone := *item
	r.definitions[item.ID] = &clone
	return nil
}

func (r *stubExportRepo) UpdateContainerTemplateDefinition(_ context.Context, item *types.ContainerTemplateDefinition) error {
	clone := *item
	r.definitions[item.ID] = &clone
	return nil
}

func (r *stubExportRepo) ListContainerTemplateDefinitionBySpecID(_ context.Context, specID int64) ([]*types.ContainerTemplateDefinition, error) {
	items := make([]*types.ContainerTemplateDefinition, 0)
	for _, definition := range r.definitions {
		if definition.SpecID == specID {
			clone := *definition
			items = append(items, &clone)
		}
	}
	return items, nil
}

func (r *stubExportRepo) ClearContainerTemplateDefinitionDefault(_ context.Context) error {
	for _, definition := range r.definitions {
		definition.IsDefault = false
	}
	return nil
}

// TestExportContainerTemplateReturnsFlatBundle 导出必须是统一平铺格式：两张表各一条，
// 且都带主键（导入依赖这些 id 保持引用关系）；镜像名与拉取策略随绑定行一起导出。
func TestExportContainerTemplateReturnsFlatBundle(t *testing.T) {
	now := time.Now()
	repo := &stubExportRepo{
		specs: map[int64]*types.ContainerTemplateSpec{
			300: {ID: 300, Name: "spec-300", Command: "R -e 1", Port: 8787, CreatedAt: &now, UpdatedAt: &now},
		},
		definitions: map[int64]*types.ContainerTemplateDefinition{
			200: {ID: 200, SpecID: 300, ImageName: "docker.io/rocker/rstudio:4.4", PullPolicy: types.PullPolicyAlways, DisplayName: "RStudio", IsDefault: true, CreatedAt: &now, UpdatedAt: &now},
		},
	}
	svc := &containerService{containerRepo: repo}

	bundle, err := svc.ExportContainerTemplate(context.Background(), 200)
	if err != nil {
		t.Fatalf("ExportContainerTemplate: %v", err)
	}
	if len(bundle.ContainerTemplateDefinitions) != 1 || bundle.ContainerTemplateDefinitions[0].ID != 200 {
		t.Fatalf("definitions = %#v", bundle.ContainerTemplateDefinitions)
	}
	if len(bundle.ContainerTemplateSpecs) != 1 || bundle.ContainerTemplateSpecs[0].ID != 300 {
		t.Fatalf("specs = %#v", bundle.ContainerTemplateSpecs)
	}
	if got := bundle.ContainerTemplateDefinitions[0].ImageName; got != "docker.io/rocker/rstudio:4.4" {
		t.Fatalf("exported image_name = %q", got)
	}
	// 导出不含时间字段（序列化结果里不出现 created_at / updated_at）。
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	if strings.Contains(string(raw), "\"created_at\"") || strings.Contains(string(raw), "\"updated_at\"") {
		t.Fatalf("bundle export must not contain timestamps: %s", raw)
	}
	// 导出不含 is_default（默认模板是本地状态）。
	if strings.Contains(string(raw), "\"is_default\"") {
		t.Fatalf("bundle export must not contain is_default: %s", raw)
	}
	// pull_policy 属于资产内容，必须随绑定行导出。
	if !strings.Contains(string(raw), "\"pull_policy\"") {
		t.Fatalf("bundle export must contain pull_policy: %s", raw)
	}
}

// TestImportContainerTemplateUpsertsByID 覆盖导入的「id 已存在则更新，不存在则按同一 id 新增」语义：
// 运行配置与绑定行不存在 → 按导出文件里的主键新增，引用关系与镜像配置保持原样。
func TestImportContainerTemplateUpsertsByID(t *testing.T) {
	repo := &stubExportRepo{
		specs:       map[int64]*types.ContainerTemplateSpec{},
		definitions: map[int64]*types.ContainerTemplateDefinition{},
		nextID:      1000,
	}
	svc := &containerService{containerRepo: repo}

	bundle, err := svc.ImportContainerTemplate(context.Background(), &types.ContainerTemplateBundle{
		ContainerTemplateSpecs: []types.ContainerTemplateSpec{
			{ID: 300, Name: "spec-300", Command: "python main.py"},
		},
		ContainerTemplateDefinitions: []types.ContainerTemplateDefinition{
			{ID: 200, SpecID: 300, ImageName: "docker.io/new:1", PullPolicy: types.PullPolicyNever, DisplayName: "imported"},
		},
	})
	if err != nil {
		t.Fatalf("ImportContainerTemplate: %v", err)
	}

	if repo.specs[300] == nil {
		t.Fatalf("spec 300 should be created: %#v", repo.specs)
	}
	definition := repo.definitions[200]
	if definition == nil {
		t.Fatalf("definition 200 should be created: %#v", repo.definitions)
	}
	if definition.SpecID != 300 || definition.ImageName != "docker.io/new:1" || definition.PullPolicy != types.PullPolicyNever {
		t.Fatalf("definition = %#v", definition)
	}

	if len(bundle.ContainerTemplateDefinitions) != 1 || bundle.ContainerTemplateDefinitions[0].ID != 200 {
		t.Fatalf("import result definitions = %#v", bundle.ContainerTemplateDefinitions)
	}
}
