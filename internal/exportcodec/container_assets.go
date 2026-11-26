package exportcodec

import (
	"context"
	"errors"
	"fmt"

	"github.com/biox-dev/gobrave/internal/types"
	"gorm.io/gorm"
)

// ===== 容器资产：两张平铺列表（script.json / workflow.json 共用）=====
//
// 两份导出文件里的容器资产都是以下两个按主键去重的平铺列表：
//
//	container_template_specs       []types.ContainerTemplateSpec
//	container_template_definitions []types.ContainerTemplateDefinition
//
// 两者通过主键互相引用（definition.spec_id → spec.id），而 definition.id 就是脚本
// container_template_id 引用的值；镜像名与拉取策略直接挂在绑定行上，因此：
//   - 导出不写 created_at / updated_at（主键才是引用键，时间戳只会产生无意义的 git diff）；
//   - 安装顺序固定为「运行配置 → 绑定行」，两者都按主键处理。
//
// 主键已存在时有两种语义，由 ContainerAssetInstallOptions 选择：
//   - 默认（覆盖）：导出文件是权威内容，已存在的行按文件更新（script.json / workflow.json 安装）；
//   - SkipExisting：只补缺失，已存在的行完全跳过（内置容器模板，用户改过的内容不被覆盖）。
//
// 本文件只放这两件事：导出侧按绑定行取资产（ExportContainerAssets）与安装侧落库（InstallContainerAssets），
// script.json、workflow.json 与内置容器模板共用同一份实现。

// ContainerAssetService 是导出 / 安装容器资产所需的最小服务能力。
//
// 定义成小接口而不是直接依赖 interfaces.ContainerRepository，
// 既避免 exportcodec 与 interfaces 互相引用，也让逻辑易于单测。
type ContainerAssetService interface {
	CreateContainerTemplateSpec(ctx context.Context, item *types.ContainerTemplateSpec) error
	GetContainerTemplateSpecByID(ctx context.Context, id int64) (*types.ContainerTemplateSpec, error)
	UpdateContainerTemplateSpec(ctx context.Context, item *types.ContainerTemplateSpec) error

	CreateContainerTemplateDefinition(ctx context.Context, item *types.ContainerTemplateDefinition) error
	GetContainerTemplateDefinitionByID(ctx context.Context, id int64) (*types.ContainerTemplateDefinition, error)
	UpdateContainerTemplateDefinition(ctx context.Context, item *types.ContainerTemplateDefinition) error
}

// ExportContainerAssets 按绑定行导出整套容器资产，返回统一平铺格式。
//
// 与 containerService.ExportContainerTemplate 同一逻辑：definitionID 就是绑定行主键
// （脚本的 container_template_id），按「绑定行 → 运行配置」各取一条；
// 取不到（绑定行已删除、spec 缺失）时只跳过，不当作错误。
// 返回的绑定行不含时间字段，可原样写入 script.json / workflow.json。
func ExportContainerAssets(ctx context.Context, svc ContainerAssetService, definitionID int64) (*types.ContainerTemplateBundle, error) {
	if definitionID == 0 || svc == nil {
		return types.NewContainerTemplateBundle(), nil
	}

	definition, err := svc.GetContainerTemplateDefinitionByID(ctx, definitionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return types.NewContainerTemplateBundle(), nil
		}
		return nil, err
	}

	bundle := types.NewContainerTemplateBundle()
	exportedDefinition := *definition
	exportedDefinition.CreatedAt = nil
	exportedDefinition.UpdatedAt = nil
	bundle.ContainerTemplateDefinitions = append(bundle.ContainerTemplateDefinitions, exportedDefinition)

	spec, err := svc.GetContainerTemplateSpecByID(ctx, definition.SpecID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if spec != nil {
		exportedSpec := *spec
		exportedSpec.CreatedAt = nil
		exportedSpec.UpdatedAt = nil
		bundle.ContainerTemplateSpecs = append(bundle.ContainerTemplateSpecs, exportedSpec)
	}

	return bundle, nil
}

// Merge 把 other 里尚未出现的资产按主键追加到 b（用于多脚本导出时去重汇总）。
func Merge(b, other *types.ContainerTemplateBundle) {
	if b == nil || other == nil {
		return
	}
	for _, definition := range other.ContainerTemplateDefinitions {
		if !hasDefinition(b, definition.ID) {
			b.ContainerTemplateDefinitions = append(b.ContainerTemplateDefinitions, definition)
		}
	}
	for _, spec := range other.ContainerTemplateSpecs {
		if !hasSpec(b, spec.ID) {
			b.ContainerTemplateSpecs = append(b.ContainerTemplateSpecs, spec)
		}
	}
}

func hasDefinition(b *types.ContainerTemplateBundle, id int64) bool {
	for _, item := range b.ContainerTemplateDefinitions {
		if item.ID == id {
			return true
		}
	}
	return false
}

func hasSpec(b *types.ContainerTemplateBundle, id int64) bool {
	for _, item := range b.ContainerTemplateSpecs {
		if item.ID == id {
			return true
		}
	}
	return false
}

// ContainerAssetInstallOptions 控制「主键已存在」时的处理方式。
type ContainerAssetInstallOptions struct {
	// SkipExisting 为 true 时只补缺失：主键已存在的行完全跳过，不做任何写入。
	//
	// 内置容器模板使用它，使每次启动只补齐被删掉的行，用户对已有行的修改得以保留；
	// 想恢复成文件里的最新定义，删掉对应行（先删绑定行再删运行配置）后重启即可。
	// 为 false（默认）时按文件覆盖已有行，供 script.json / workflow.json 安装使用。
	SkipExisting bool
}

// ContainerAssetInstallResult 是安装容器资产的结果。
type ContainerAssetInstallResult struct {
	// SpecCount / DefinitionCount 是文件里出现的行数（含已存在而被跳过的）。
	SpecCount       int
	DefinitionCount int
	// CreatedSpecCount / CreatedDefinitionCount 是本次真正新增的行数，
	// 因此 SpecCount-CreatedSpecCount 就是 SkipExisting 下跳过的行数。
	CreatedSpecCount       int
	CreatedDefinitionCount int
}

// InstallContainerAssets 按主键安装两张表（已存在则按文件覆盖），返回各类数量。
//
// 安装顺序固定为「运行配置 → 绑定行」：绑定行引用运行配置，id 为 0 视为导出文件损坏直接报错
// （id 是唯一的引用键）。
func InstallContainerAssets(ctx context.Context, svc ContainerAssetService, specs []types.ContainerTemplateSpec, definitions []types.ContainerTemplateDefinition) (*ContainerAssetInstallResult, error) {
	return InstallContainerAssetsWithOptions(ctx, svc, specs, definitions, ContainerAssetInstallOptions{})
}

// InstallContainerAssetsWithOptions 是 InstallContainerAssets 的可配置版本，
// 由 opts 决定「主键已存在」时覆盖还是跳过（语义见 ContainerAssetInstallOptions）。
func InstallContainerAssetsWithOptions(ctx context.Context, svc ContainerAssetService, specs []types.ContainerTemplateSpec, definitions []types.ContainerTemplateDefinition, opts ContainerAssetInstallOptions) (*ContainerAssetInstallResult, error) {
	if len(specs) == 0 && len(definitions) == 0 {
		return &ContainerAssetInstallResult{}, nil
	}
	if svc == nil {
		return nil, errors.New("container service is not configured")
	}

	result := &ContainerAssetInstallResult{}

	for i := range specs {
		if specs[i].ID == 0 {
			return nil, errors.New("container template spec id is required in export file")
		}
		created, err := installContainerTemplateSpec(ctx, svc, &specs[i], opts)
		if err != nil {
			return nil, fmt.Errorf("failed to install container template spec %d: %w", specs[i].ID, err)
		}
		result.SpecCount++
		if created {
			result.CreatedSpecCount++
		}
	}

	for i := range definitions {
		if definitions[i].ID == 0 {
			return nil, errors.New("container template definition id is required in export file")
		}
		created, err := installContainerTemplateDefinition(ctx, svc, &definitions[i], opts)
		if err != nil {
			return nil, fmt.Errorf("failed to install container template definition %d: %w", definitions[i].ID, err)
		}
		result.DefinitionCount++
		if created {
			result.CreatedDefinitionCount++
		}
	}

	return result, nil
}

// installContainerTemplateSpec 安装一条运行配置，返回是否新建。
// 主键不存在则按同一主键新增；已存在时按 opts 覆盖或跳过。
func installContainerTemplateSpec(ctx context.Context, svc ContainerAssetService, spec *types.ContainerTemplateSpec, opts ContainerAssetInstallOptions) (bool, error) {
	if _, err := svc.GetContainerTemplateSpecByID(ctx, spec.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, svc.CreateContainerTemplateSpec(ctx, spec)
		}
		return false, err
	}
	if opts.SkipExisting {
		return false, nil
	}
	return false, svc.UpdateContainerTemplateSpec(ctx, spec)
}

// installContainerTemplateDefinition 安装一条绑定行，返回是否新建。
// 主键不存在则按同一主键新增；已存在时按 opts 覆盖或跳过。
func installContainerTemplateDefinition(ctx context.Context, svc ContainerAssetService, definition *types.ContainerTemplateDefinition, opts ContainerAssetInstallOptions) (bool, error) {
	if _, err := svc.GetContainerTemplateDefinitionByID(ctx, definition.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, svc.CreateContainerTemplateDefinition(ctx, definition)
		}
		return false, err
	}
	if opts.SkipExisting {
		return false, nil
	}
	return false, svc.UpdateContainerTemplateDefinition(ctx, definition)
}
