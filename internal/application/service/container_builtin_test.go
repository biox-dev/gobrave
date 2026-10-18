package service

import (
	"context"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// fakeBuiltinContainerService 只实现内置模板导入用到的方法：
// 安装侧（InstallContainerAssets 的 Create / Get / Update）与默认标记（SetDefaultContainerTemplate）。
// 其余方法由嵌入的（nil）接口提供，一旦被测代码走到未实现的方法会直接 panic。
type fakeBuiltinContainerService struct {
	interfaces.ContainerService
	images      map[int64]*types.ContainerImage
	specs       map[int64]*types.ContainerTemplateSpec
	definitions map[int64]*types.ContainerTemplateDefinition
	// defaultID 记录最后一次被设为默认的绑定行（0 表示从未设置）。
	defaultID int64
}

func newFakeBuiltinContainerService() *fakeBuiltinContainerService {
	return &fakeBuiltinContainerService{
		images:      make(map[int64]*types.ContainerImage),
		specs:       make(map[int64]*types.ContainerTemplateSpec),
		definitions: make(map[int64]*types.ContainerTemplateDefinition),
	}
}

func (f *fakeBuiltinContainerService) CreateContainerImage(_ context.Context, item *types.ContainerImage) error {
	clone := *item
	f.images[item.ID] = &clone
	return nil
}

func (f *fakeBuiltinContainerService) GetContainerImageByID(_ context.Context, id int64) (*types.ContainerImage, error) {
	item, ok := f.images[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	clone := *item
	return &clone, nil
}

func (f *fakeBuiltinContainerService) UpdateContainerImage(_ context.Context, item *types.ContainerImage) error {
	clone := *item
	f.images[item.ID] = &clone
	return nil
}

func (f *fakeBuiltinContainerService) CreateContainerTemplateSpec(_ context.Context, item *types.ContainerTemplateSpec) error {
	clone := *item
	f.specs[item.ID] = &clone
	return nil
}

func (f *fakeBuiltinContainerService) GetContainerTemplateSpecByID(_ context.Context, id int64) (*types.ContainerTemplateSpec, error) {
	item, ok := f.specs[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	clone := *item
	return &clone, nil
}

func (f *fakeBuiltinContainerService) UpdateContainerTemplateSpec(_ context.Context, item *types.ContainerTemplateSpec) error {
	clone := *item
	f.specs[item.ID] = &clone
	return nil
}

func (f *fakeBuiltinContainerService) CreateContainerTemplateDefinition(_ context.Context, item *types.ContainerTemplateDefinition) error {
	clone := *item
	f.definitions[item.ID] = &clone
	return nil
}

func (f *fakeBuiltinContainerService) GetContainerTemplateDefinitionByID(_ context.Context, id int64) (*types.ContainerTemplateDefinition, error) {
	item, ok := f.definitions[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	clone := *item
	return &clone, nil
}

func (f *fakeBuiltinContainerService) UpdateContainerTemplateDefinition(_ context.Context, item *types.ContainerTemplateDefinition) error {
	clone := *item
	f.definitions[item.ID] = &clone
	return nil
}

func (f *fakeBuiltinContainerService) SetDefaultContainerTemplate(_ context.Context, definitionID int64) error {
	f.defaultID = definitionID
	return nil
}

// TestInstallBuiltinContainerTemplatesImportsEmbeddedFiles 内置模板必须能被解析并导入：
// 两个内嵌 JSON（code-server-quarto-r / rstudio-tidyverse-r）都按导出文件里的主键落库，
// 且文件里 is_default=true 的绑定行会被设为默认模板。
func TestInstallBuiltinContainerTemplatesImportsEmbeddedFiles(t *testing.T) {
	// 指向不存在的目录 → 回退到 go:embed 内嵌副本。
	SetBuiltinContainerTemplateDir(t.TempDir())
	t.Cleanup(func() { SetBuiltinContainerTemplateDir("") })

	svc := newFakeBuiltinContainerService()
	if err := InstallBuiltinContainerTemplates(context.Background(), svc); err != nil {
		t.Fatalf("InstallBuiltinContainerTemplates: %v", err)
	}

	// code-server-quarto-r
	if image := svc.images[2089923441674162176]; image == nil {
		t.Fatalf("code-server image not installed: %#v", svc.images)
	} else if image.Name != "code-server-quarto-r" {
		t.Fatalf("code-server image name = %q", image.Name)
	}
	if spec := svc.specs[2089923556677783552]; spec == nil || spec.Port != 8443 || spec.AppType != "code-server" {
		t.Fatalf("code-server spec = %#v", spec)
	}

	// rstudio-tidyverse-r
	if image := svc.images[202606160001]; image == nil {
		t.Fatalf("rstudio image not installed: %#v", svc.images)
	}
	definition := svc.definitions[202606160002]
	if definition == nil {
		t.Fatalf("rstudio definition not installed: %#v", svc.definitions)
	}
	if definition.SpecID != 202606160002 || definition.ImageID != 202606160001 {
		t.Fatalf("rstudio definition refs = (%d,%d)", definition.SpecID, definition.ImageID)
	}

	if len(svc.images) != 2 || len(svc.specs) != 2 || len(svc.definitions) != 2 {
		t.Fatalf("counts = (%d,%d,%d), want (2,2,2)",
			len(svc.images), len(svc.specs), len(svc.definitions))
	}

	// 只有 rstudio 标记了默认；code-server 的 is_default=false 不应设置默认。
	if svc.defaultID != 202606160002 {
		t.Fatalf("defaultID = %d, want 202606160002", svc.defaultID)
	}
}

// TestInstallBuiltinContainerTemplatesIsIdempotent 每次系统启动都会调用一次，
// 因此重复导入必须是「按主键更新」而不是重复新增。
func TestInstallBuiltinContainerTemplatesIsIdempotent(t *testing.T) {
	SetBuiltinContainerTemplateDir(t.TempDir())
	t.Cleanup(func() { SetBuiltinContainerTemplateDir("") })

	svc := newFakeBuiltinContainerService()
	for i := 0; i < 2; i++ {
		if err := InstallBuiltinContainerTemplates(context.Background(), svc); err != nil {
			t.Fatalf("run %d: InstallBuiltinContainerTemplates: %v", i, err)
		}
	}

	if len(svc.images) != 2 || len(svc.specs) != 2 || len(svc.definitions) != 2 {
		t.Fatalf("counts after re-install = (%d,%d,%d), want (2,2,2)",
			len(svc.images), len(svc.specs), len(svc.definitions))
	}
}

// TestInstallBuiltinContainerTemplatesNilService 没有容器服务时直接返回，不应报错。
func TestInstallBuiltinContainerTemplatesNilService(t *testing.T) {
	if err := InstallBuiltinContainerTemplates(context.Background(), nil); err != nil {
		t.Fatalf("InstallBuiltinContainerTemplates(nil): %v", err)
	}
}
