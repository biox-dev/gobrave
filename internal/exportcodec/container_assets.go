package exportcodec

import (
	"context"
	"errors"
	"fmt"

	"github.com/biox-dev/gobrave/internal/types"
	"gorm.io/gorm"
)

// ===== 容器资产：三张平铺列表（script.json / workflow.json 共用）=====
//
// 两份导出文件里的容器资产都是以下三个按主键去重的平铺列表：
//
//	container_images               []types.ContainerImageExport
//	container_template_specs       []types.ContainerTemplateSpec
//	container_template_definitions []types.ContainerTemplateDefinition
//
// 三者通过主键互相引用（definition.spec_id → spec.id，definition.image_id → image.id，
// 而 definition.id 就是脚本 container_template_id 引用的值），因此：
//   - 导出不写 created_at / updated_at（主键才是引用键，时间戳只会产生无意义的 git diff）；
//   - 安装顺序固定为「镜像 → 运行配置 → 绑定行」，三者都按主键 upsert。
//
// 本文件只放这两件事：导出侧按绑定行取资产（ExportContainerAssets）与安装侧落库（InstallContainerAssets），
// script.json、workflow.json 与容器模板导出共用同一份实现。

// ContainerAssetService 是导出 / 安装容器资产所需的最小服务能力。
//
// 定义成小接口而不是直接依赖 interfaces.ContainerRepository，
// 既避免 exportcodec 与 interfaces 互相引用，也让逻辑易于单测。
// 这里列出的是导出侧要用的读取方法；安装侧只用到 Create / Update 方法，
// 合并成一个接口是因为 repository 实现本来就同时具备。
type ContainerAssetService interface {
	CreateContainerImage(ctx context.Context, item *types.ContainerImage) error
	GetContainerImageByID(ctx context.Context, id int64) (*types.ContainerImage, error)
	UpdateContainerImage(ctx context.Context, item *types.ContainerImage) error

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
// （脚本的 container_template_id），按「绑定行 → 运行配置 → 镜像」各取一条；
// 取不到（绑定行已删除、spec / image 缺失）时只跳过，不当作错误。
// 返回的绑定行固定 is_default=false 且不含时间字段，可原样写入 script.json / workflow.json。
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
	exportedDefinition.IsDefault = false
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

	image, err := svc.GetContainerImageByID(ctx, definition.ImageID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if image != nil {
		bundle.ContainerImages = append(bundle.ContainerImages, *image.ToExport())
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
	for _, image := range other.ContainerImages {
		if !hasImage(b, image.ID) {
			b.ContainerImages = append(b.ContainerImages, image)
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

func hasImage(b *types.ContainerTemplateBundle, id int64) bool {
	for _, item := range b.ContainerImages {
		if item.ID == id {
			return true
		}
	}
	return false
}

// ContainerAssetInstallResult 是安装容器资产的结果（按主键 upsert 的数量）。
type ContainerAssetInstallResult struct {
	ImageCount      int
	SpecCount       int
	DefinitionCount int
}

// InstallContainerAssets 按主键 upsert 三张表，返回各类数量。
//
// 安装顺序固定为「镜像 → 运行配置 → 绑定行」：绑定行同时引用另外两张表，
// service 层也会校验镜像存在。id 为 0 视为导出文件损坏直接报错（id 是唯一的引用键）。
func InstallContainerAssets(ctx context.Context, svc ContainerAssetService, images []types.ContainerImageExport, specs []types.ContainerTemplateSpec, definitions []types.ContainerTemplateDefinition) (*ContainerAssetInstallResult, error) {
	if len(images) == 0 && len(specs) == 0 && len(definitions) == 0 {
		return &ContainerAssetInstallResult{}, nil
	}
	if svc == nil {
		return nil, errors.New("container service is not configured")
	}

	for i := range images {
		if images[i].ID == 0 {
			return nil, errors.New("container image id is required in export file")
		}
		if err := upsertContainerImage(ctx, svc, &images[i]); err != nil {
			return nil, fmt.Errorf("failed to install container image %d: %w", images[i].ID, err)
		}
	}

	for i := range specs {
		if specs[i].ID == 0 {
			return nil, errors.New("container template spec id is required in export file")
		}
		if err := upsertContainerTemplateSpec(ctx, svc, &specs[i]); err != nil {
			return nil, fmt.Errorf("failed to install container template spec %d: %w", specs[i].ID, err)
		}
	}

	for i := range definitions {
		if definitions[i].ID == 0 {
			return nil, errors.New("container template definition id is required in export file")
		}
		if err := upsertContainerTemplateDefinition(ctx, svc, &definitions[i]); err != nil {
			return nil, fmt.Errorf("failed to install container template definition %d: %w", definitions[i].ID, err)
		}
	}

	return &ContainerAssetInstallResult{
		ImageCount:      len(images),
		SpecCount:       len(specs),
		DefinitionCount: len(definitions),
	}, nil
}

// upsertContainerImage 按主键更新镜像，主键不存在则按同一主键新增（保持引用关系）。
// 导出文件不含 pull_policy，新增时按默认值 IfNotPresent；更新时保留库里的原值。
func upsertContainerImage(ctx context.Context, svc ContainerAssetService, export *types.ContainerImageExport) error {
	item := &types.ContainerImage{
		ID:          export.ID,
		Name:        export.Name,
		FullName:    export.FullName,
		Description: export.Description,
		Size:        export.Size,
		PullPolicy:  types.PullPolicyIfNotPresent,
	}

	if existing, err := svc.GetContainerImageByID(ctx, item.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return svc.CreateContainerImage(ctx, item)
		}
		return err
	} else if existing != nil && existing.PullPolicy != "" {
		// 更新时不要用默认值覆盖库里已有的拉取策略。
		item.PullPolicy = existing.PullPolicy
	}
	return svc.UpdateContainerImage(ctx, item)
}

// upsertContainerTemplateSpec 按主键更新运行配置，主键不存在则按同一主键新增。
func upsertContainerTemplateSpec(ctx context.Context, svc ContainerAssetService, spec *types.ContainerTemplateSpec) error {
	if _, err := svc.GetContainerTemplateSpecByID(ctx, spec.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return svc.CreateContainerTemplateSpec(ctx, spec)
		}
		return err
	}
	return svc.UpdateContainerTemplateSpec(ctx, spec)
}

// upsertContainerTemplateDefinition 按主键更新绑定行，主键不存在则按同一主键新增。
func upsertContainerTemplateDefinition(ctx context.Context, svc ContainerAssetService, definition *types.ContainerTemplateDefinition) error {
	if _, err := svc.GetContainerTemplateDefinitionByID(ctx, definition.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return svc.CreateContainerTemplateDefinition(ctx, definition)
		}
		return err
	}
	return svc.UpdateContainerTemplateDefinition(ctx, definition)
}
