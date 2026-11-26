package service

import (
	"context"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// fakeBuiltinContainerService 只实现内置模板导入用到的方法：
// 安装侧（InstallContainerAssets 的 Create / Get / Update）。
// 其余方法由嵌入的（nil）接口提供，一旦被测代码走到未实现的方法会直接 panic。
type fakeBuiltinContainerService struct {
	interfaces.ContainerService
	specs       map[int64]*types.ContainerTemplateSpec
	definitions map[int64]*types.ContainerTemplateDefinition
}

func newFakeBuiltinContainerService() *fakeBuiltinContainerService {
	return &fakeBuiltinContainerService{
		specs:       make(map[int64]*types.ContainerTemplateSpec),
		definitions: make(map[int64]*types.ContainerTemplateDefinition),
	}
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

// TestInstallBuiltinContainerTemplatesImportsEmbeddedFiles 内置模板必须能被解析并导入：
// 内嵌 JSON（code-server-quarto-r / rstudio-tidyverse-r / ubuntu）都按导出文件里的主键落库。
func TestInstallBuiltinContainerTemplatesImportsEmbeddedFiles(t *testing.T) {
	// 指向不存在的目录 → 回退到 go:embed 内嵌副本。
	SetBuiltinContainerTemplateDir(t.TempDir())
	t.Cleanup(func() { SetBuiltinContainerTemplateDir("") })

	svc := newFakeBuiltinContainerService()
	if err := InstallBuiltinContainerTemplates(context.Background(), svc); err != nil {
		t.Fatalf("InstallBuiltinContainerTemplates: %v", err)
	}

	// code-server-quarto-r
	if spec := svc.specs[2089923556677783552]; spec == nil || spec.Port != 8443 || spec.AppType != "code-server" {
		t.Fatalf("code-server spec = %#v", spec)
	}
	if def := svc.definitions[2089923556677783552]; def == nil || def.ImageName == "" {
		t.Fatalf("code-server definition = %#v", def)
	}

	// rstudio-tidyverse-r
	definition := svc.definitions[202606160002]
	if definition == nil {
		t.Fatalf("rstudio definition not installed: %#v", svc.definitions)
	}
	if definition.SpecID != 202606160002 || definition.ImageName != "registry.cn-hangzhou.aliyuncs.com/wybioinfo/rocker-tidyverse-r:4.6.0" {
		t.Fatalf("rstudio definition = %#v", definition)
	}

	// ubuntu
	if def := svc.definitions[2112483278714834944]; def == nil || def.ImageName != "registry.cn-hangzhou.aliyuncs.com/wybioinfo/ubuntu" {
		t.Fatalf("ubuntu definition = %#v", def)
	}

	if len(svc.specs) != 3 || len(svc.definitions) != 3 {
		t.Fatalf("counts = (%d,%d), want (3,3)", len(svc.specs), len(svc.definitions))
	}
}

// TestInstallBuiltinContainerTemplatesKeepsExistingRows 每次系统启动都会调用一次，
// 但导入语义是「只补缺失」：主键已存在的行不会被文件覆盖（用户改过的内容得以保留），
// 重复导入也不会重复新增。
func TestInstallBuiltinContainerTemplatesKeepsExistingRows(t *testing.T) {
	SetBuiltinContainerTemplateDir(t.TempDir())
	t.Cleanup(func() { SetBuiltinContainerTemplateDir("") })

	const (
		ubuntuSpecID       int64 = 2112483153338699776
		ubuntuDefinitionID int64 = 2112483278714834944
	)

	svc := newFakeBuiltinContainerService()
	// 预置一份「被用户改过」的内置行：重启后必须原样保留。
	svc.specs[ubuntuSpecID] = &types.ContainerTemplateSpec{ID: ubuntuSpecID, Name: "user-edited", Command: "sleep 1", Port: 1234}
	svc.definitions[ubuntuDefinitionID] = &types.ContainerTemplateDefinition{ID: ubuntuDefinitionID, SpecID: ubuntuSpecID, ImageName: "user/ubuntu:1"}

	for i := 0; i < 2; i++ {
		if err := InstallBuiltinContainerTemplates(context.Background(), svc); err != nil {
			t.Fatalf("run %d: InstallBuiltinContainerTemplates: %v", i, err)
		}
	}

	if spec := svc.specs[ubuntuSpecID]; spec.Name != "user-edited" || spec.Command != "sleep 1" || spec.Port != 1234 {
		t.Fatalf("existing spec was overwritten: %#v", spec)
	}
	if def := svc.definitions[ubuntuDefinitionID]; def.ImageName != "user/ubuntu:1" {
		t.Fatalf("existing definition was overwritten: %#v", def)
	}

	// 缺失的行仍然被补齐，且不会因为重复导入而重复新增。
	if len(svc.specs) != 3 || len(svc.definitions) != 3 {
		t.Fatalf("counts after re-install = (%d,%d), want (3,3)", len(svc.specs), len(svc.definitions))
	}
}

// TestInstallBuiltinContainerTemplatesRecreatesDeletedRows 「想更新内置模板」的操作方式是
// 删掉对应的行（先删绑定行，再删运行配置）后重启：缺失的主键会被重新写入文件里的定义。
func TestInstallBuiltinContainerTemplatesRecreatesDeletedRows(t *testing.T) {
	SetBuiltinContainerTemplateDir(t.TempDir())
	t.Cleanup(func() { SetBuiltinContainerTemplateDir("") })

	const (
		ubuntuSpecID       int64 = 2112483153338699776
		ubuntuDefinitionID int64 = 2112483278714834944
	)

	svc := newFakeBuiltinContainerService()
	if err := InstallBuiltinContainerTemplates(context.Background(), svc); err != nil {
		t.Fatalf("InstallBuiltinContainerTemplates: %v", err)
	}

	// 模拟用户删除「运行配置 × 镜像」绑定行与共享运行配置。
	delete(svc.definitions, ubuntuDefinitionID)
	delete(svc.specs, ubuntuSpecID)

	if err := InstallBuiltinContainerTemplates(context.Background(), svc); err != nil {
		t.Fatalf("InstallBuiltinContainerTemplates after delete: %v", err)
	}

	if spec := svc.specs[ubuntuSpecID]; spec == nil || spec.Name != "bioinfo" {
		t.Fatalf("deleted spec not recreated: %#v", spec)
	}
	if def := svc.definitions[ubuntuDefinitionID]; def == nil || def.ImageName != "registry.cn-hangzhou.aliyuncs.com/wybioinfo/ubuntu" || def.SpecID != ubuntuSpecID {
		t.Fatalf("deleted definition not recreated: %#v", def)
	}
}

// TestInstallBuiltinContainerTemplatesNilService 没有容器服务时直接返回，不应报错。
func TestInstallBuiltinContainerTemplatesNilService(t *testing.T) {
	if err := InstallBuiltinContainerTemplates(context.Background(), nil); err != nil {
		t.Fatalf("InstallBuiltinContainerTemplates(nil): %v", err)
	}
}
