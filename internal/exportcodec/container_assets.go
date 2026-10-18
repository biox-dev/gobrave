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
// 本文件只放这两件事：导出侧去重收集（ContainerAssetBundle）与安装侧落库（InstallContainerAssets），
// script.json 与 workflow.json 共用同一份实现。

// ContainerAssetBundle 是导出侧收集容器资产的统一载体。
//
// workflowService 遍历脚本时用它去重收集（同一镜像 / 运行配置 / 绑定行只收一次），
// 最后一次性写入 payload，保证 script.json 与 workflow.json 的容器资产口径完全一致。
type ContainerAssetBundle struct {
	Images      []types.ContainerImageExport
	Specs       []types.ContainerTemplateSpec
	Definitions []types.ContainerTemplateDefinition
}

// NewContainerAssetBundle 返回空 bundle（切片非 nil，序列化为 [] 而不是 null）。
func NewContainerAssetBundle() *ContainerAssetBundle {
	return &ContainerAssetBundle{
		Images:      make([]types.ContainerImageExport, 0),
		Specs:       make([]types.ContainerTemplateSpec, 0),
		Definitions: make([]types.ContainerTemplateDefinition, 0),
	}
}

// IsEmpty 判断 bundle 是否没有任何容器资产。
func (b *ContainerAssetBundle) IsEmpty() bool {
	return b == nil || (len(b.Images) == 0 && len(b.Specs) == 0 && len(b.Definitions) == 0)
}

// HasDefinition 判断绑定行是否已收集（按主键去重）。
func (b *ContainerAssetBundle) HasDefinition(id int64) bool {
	if b == nil {
		return false
	}
	for i := range b.Definitions {
		if b.Definitions[i].ID == id {
			return true
		}
	}
	return false
}

// HasSpec 判断运行配置是否已收集（按主键去重）。
func (b *ContainerAssetBundle) HasSpec(id int64) bool {
	if b == nil {
		return false
	}
	for i := range b.Specs {
		if b.Specs[i].ID == id {
			return true
		}
	}
	return false
}

// HasImage 判断镜像是否已收集（按主键去重）。
func (b *ContainerAssetBundle) HasImage(id int64) bool {
	if b == nil {
		return false
	}
	for i := range b.Images {
		if b.Images[i].ID == id {
			return true
		}
	}
	return false
}

// AttachTo 把 bundle 的三个列表写入 script.json / workflow.json 的对应字段（剔除时间字段）。
// bundle 为 nil 时写成空切片；目标字段必须非 nil。
func (b *ContainerAssetBundle) AttachTo(specs *[]types.ContainerTemplateSpec, definitions *[]types.ContainerTemplateDefinition, images *[]types.ContainerImageExport) {
	if specs == nil || definitions == nil || images == nil {
		return
	}
	if b == nil {
		*specs = make([]types.ContainerTemplateSpec, 0)
		*definitions = make([]types.ContainerTemplateDefinition, 0)
		*images = make([]types.ContainerImageExport, 0)
		return
	}
	*specs = omitSpecTimestamps(b.Specs)
	*definitions = omitDefinitionTimestamps(b.Definitions)
	*images = b.Images
}

// ContainerAssetService 是安装容器资产所需的最小服务能力。
//
// 定义成小接口而不是直接依赖 interfaces.ContainerService：
// 既避免 exportcodec 与 interfaces 互相引用，也让安装逻辑易于单测。
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

// omitSpecTimestamps 返回剔除时间字段的副本（导出文件不写 created_at / updated_at）。
func omitSpecTimestamps(items []types.ContainerTemplateSpec) []types.ContainerTemplateSpec {
	out := make([]types.ContainerTemplateSpec, len(items))
	for i, item := range items {
		item.CreatedAt = nil
		item.UpdatedAt = nil
		out[i] = item
	}
	return out
}

// omitDefinitionTimestamps 返回剔除时间字段的副本。
func omitDefinitionTimestamps(items []types.ContainerTemplateDefinition) []types.ContainerTemplateDefinition {
	out := make([]types.ContainerTemplateDefinition, len(items))
	for i, item := range items {
		item.CreatedAt = nil
		item.UpdatedAt = nil
		out[i] = item
	}
	return out
}
