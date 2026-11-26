package service

import (
	"context"
	stderrs "errors"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	apperrors "github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type containerService struct {
	containerRepo interfaces.ContainerRepository
	containerMgr  *manager.ContainerManager
	cfg           *config.Config
}

// var ()

func NewContainerService(containerRepo interfaces.ContainerRepository, containerMgr *manager.ContainerManager, config *config.Config) interfaces.ContainerService {
	return &containerService{containerRepo: containerRepo, containerMgr: containerMgr, cfg: config}
}

// ExportContainerTemplate 导出单条容器模板，返回统一平铺格式（与 script.json / workflow.json 相同）：
// container_template_specs / container_template_definitions 各一条。
// 两个列表都不含时间字段，可直接交给 ImportContainerTemplate 原样导入。
func (s *containerService) ExportContainerTemplate(ctx context.Context, definitionID int64) (*types.ContainerTemplateBundle, error) {
	if definitionID == 0 {
		return nil, fmt.Errorf("container template id is required")
	}
	return exportcodec.ExportContainerAssets(ctx, s.containerRepo, definitionID)
}

// normalizePullPolicy 归一化拉取策略：空值与无法识别的值统一按 IfNotPresent 处理。
func normalizePullPolicy(policy string) string {
	switch policy {
	case types.PullPolicyAlways, types.PullPolicyIfNotPresent, types.PullPolicyNever:
		return policy
	default:
		return types.PullPolicyIfNotPresent
	}
}

// applyDefaultFlag 把「某绑定行为默认」写入库：先清空其它行的默认标记，再置位目标行。
// 默认模板必须全局唯一（创建脚本未指定 container_template_id 时依赖它取默认值）。
// definitionID 为 0 表示不改变默认标记。
func (s *containerService) applyDefaultFlag(ctx context.Context, tx interfaces.ContainerRepository, definitionID int64) error {
	if definitionID == 0 {
		return nil
	}
	if err := tx.ClearContainerTemplateDefinitionDefault(ctx); err != nil {
		return err
	}
	// 先读出整行再置位：绑定行的更新是整行语义（name 支持显式清空），
	// 只传 ID + IsDefault 会把其它字段覆盖成零值。
	binding, err := tx.GetContainerTemplateDefinitionByID(ctx, definitionID)
	if err != nil {
		return err
	}
	binding.IsDefault = true
	return tx.UpdateContainerTemplateDefinition(ctx, binding)
}

// GetDefaultContainerTemplate 返回默认容器模板（对外读模型）；没有默认行时返回 (nil, nil)。
func (s *containerService) GetDefaultContainerTemplate(ctx context.Context) (*types.ContainerTemplate, error) {
	definition, err := s.containerRepo.GetDefaultContainerTemplateDefinition(ctx)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return s.containerRepo.GetContainerTemplateByID(ctx, definition.ID)
}

// SetDefaultContainerTemplate 把指定绑定行设为唯一默认模板。
func (s *containerService) SetDefaultContainerTemplate(ctx context.Context, definitionID int64) error {
	if definitionID <= 0 {
		return fmt.Errorf("container template definition id is required")
	}
	if _, err := s.containerRepo.GetContainerTemplateDefinitionByID(ctx, definitionID); err != nil {
		return err
	}
	return s.containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		return s.applyDefaultFlag(ctx, tx, definitionID)
	})
}

// EnsureDefaultContainerTemplate 在没有任何默认模板时，把库里最早的绑定行设为默认。
//
// 仅在服务启动时调用（存量数据没有默认标记，否则 create_script 无法解析默认值）。
// 已存在默认行时不做任何改动，因此不会覆盖用户在 UI 上的选择。
func EnsureDefaultContainerTemplate(ctx context.Context, containerRepo interfaces.ContainerRepository) error {
	if containerRepo == nil {
		return nil
	}
	s := &containerService{containerRepo: containerRepo}

	defaultDefinition, err := containerRepo.GetDefaultContainerTemplateDefinition(ctx)
	if err == nil && defaultDefinition != nil {
		return nil
	}
	if err != nil && !stderrs.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	templates, err := containerRepo.ListContainerTemplate(ctx)
	if err != nil {
		return err
	}
	if len(templates) == 0 {
		return nil
	}

	candidateID := templates[len(templates)-1].ID // ListContainerTemplate 按 id DESC 返回，末尾是最早的一条
	return containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		return s.applyDefaultFlag(ctx, tx, candidateID)
	})
}

// ensureImageNameNotBound 防止同一套配置重复绑定同一个镜像（DB 里也有唯一索引兜底）。
func (s *containerService) ensureImageNameNotBound(ctx context.Context, specID int64, imageName string) error {
	imageName = strings.TrimSpace(imageName)
	if imageName == "" {
		return fmt.Errorf("image name is required")
	}
	bindings, err := s.containerRepo.ListContainerTemplateDefinitionBySpecID(ctx, specID)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if strings.TrimSpace(binding.ImageName) == imageName {
			return fmt.Errorf("image %q is already bound to container template spec %d", imageName, specID)
		}
	}
	return nil
}

func (s *containerService) GetContainerTemplateByID(ctx context.Context, id int64) (*types.ContainerTemplate, error) {
	return s.containerRepo.GetContainerTemplateByID(ctx, id)
}

// ensureImageName 校验绑定行的镜像名非空（镜像名直接存在绑定行上）。
func ensureImageName(imageName string) error {
	if strings.TrimSpace(imageName) == "" {
		return fmt.Errorf("image name is required")
	}
	return nil
}

// DeleteContainerTemplateDefinition 按主键删除一条绑定行；共享运行配置（ContainerTemplateSpec）保留。
func (s *containerService) DeleteContainerTemplateDefinition(ctx context.Context, id int64) error {
	binding, err := s.containerRepo.GetContainerTemplateDefinitionByID(ctx, id)
	if err != nil {
		return err
	}
	return s.containerRepo.DeleteContainerTemplateDefinition(ctx, binding.ID)
}

func (s *containerService) ListContainerTemplate(ctx context.Context) ([]*types.ContainerTemplate, error) {
	return s.containerRepo.ListContainerTemplate(ctx)
}

// ListContainerTemplateBySpecID 返回同一套共享运行配置下绑定不同镜像的所有对外模板。
func (s *containerService) ListContainerTemplateBySpecID(ctx context.Context, specID int64) ([]*types.ContainerTemplate, error) {
	if specID == 0 {
		return nil, fmt.Errorf("container template spec id is required")
	}
	if _, err := s.containerRepo.GetContainerTemplateSpecByID(ctx, specID); err != nil {
		return nil, err
	}
	return s.containerRepo.ListContainerTemplateBySpecID(ctx, specID)
}

func (s *containerService) PageContainerTemplate(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageContainerTemplate(ctx, pagination)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

// ImportContainerTemplate 导入统一平铺格式（与 script.json / workflow.json 相同）：
// container_template_specs / container_template_definitions。
//
// 两张表都按主键 upsert（id 已存在则更新，不存在则按同一 id 新增），顺序固定为
// 「运行配置 → 绑定行」；导入完成后把所有绑定行去重汇总成返回值。
// is_default=true 的绑定行额外走互斥的默认标记流程。
func (s *containerService) ImportContainerTemplate(ctx context.Context, bundle *types.ContainerTemplateBundle) (*types.ContainerTemplateBundle, error) {
	if bundle == nil {
		return nil, fmt.Errorf("container template bundle is required")
	}

	if _, err := exportcodec.InstallContainerAssets(ctx, s.containerRepo,
		bundle.ContainerTemplateSpecs, bundle.ContainerTemplateDefinitions); err != nil {
		return nil, err
	}

	for _, definition := range bundle.ContainerTemplateDefinitions {
		if definition.IsDefault {
			if err := s.applyDefaultFlag(ctx, s.containerRepo, definition.ID); err != nil {
				return nil, err
			}
			break
		}
	}

	result := types.NewContainerTemplateBundle()
	seen := make(map[int64]struct{}, len(bundle.ContainerTemplateDefinitions))
	for _, definition := range bundle.ContainerTemplateDefinitions {
		if _, exists := seen[definition.ID]; exists {
			continue
		}
		seen[definition.ID] = struct{}{}

		exported, err := s.ExportContainerTemplate(ctx, definition.ID)
		if err != nil {
			return nil, err
		}
		exportcodec.Merge(result, exported)
	}
	return result, nil
}

// ===== 容器模板：按主键直接 upsert 两层实体（供 script.json / workflow.json 安装使用）=====

// CreateContainerTemplateSpec 新增一条共享运行配置（安装导出内容时主键由导出文件带入）。
func (s *containerService) CreateContainerTemplateSpec(ctx context.Context, item *types.ContainerTemplateSpec) error {
	if item == nil {
		return fmt.Errorf("container template spec is required")
	}
	return s.containerRepo.CreateContainerTemplateSpec(ctx, item)
}

func (s *containerService) GetContainerTemplateSpecByID(ctx context.Context, id int64) (*types.ContainerTemplateSpec, error) {
	return s.containerRepo.GetContainerTemplateSpecByID(ctx, id)
}

// UpdateContainerTemplateSpec 按主键更新共享运行配置（不动 created_at）。
func (s *containerService) UpdateContainerTemplateSpec(ctx context.Context, item *types.ContainerTemplateSpec) error {
	if item == nil || item.ID == 0 {
		return fmt.Errorf("container template spec id is required")
	}
	return s.containerRepo.UpdateContainerTemplateSpec(ctx, item)
}

// DeleteContainerTemplateSpec 删除共享运行配置。
// 仍被绑定行引用时拒绝删除（返回 409），避免绑定行的 spec_id 变成空引用；
// 需要强制删除时先删掉这些绑定行（DeleteContainerTemplateDefinition）。
func (s *containerService) DeleteContainerTemplateSpec(ctx context.Context, id int64) error {
	bindings, err := s.containerRepo.ListContainerTemplateDefinitionBySpecID(ctx, id)
	if err != nil {
		return err
	}
	if len(bindings) > 0 {
		return apperrors.NewConflictError(
			fmt.Sprintf("container template spec %d is referenced by %d definition(s)", id, len(bindings)),
		)
	}
	return s.containerRepo.DeleteContainerTemplateSpec(ctx, id)
}

// ListContainerTemplateSpec 列出全部共享运行配置。
func (s *containerService) ListContainerTemplateSpec(ctx context.Context) ([]*types.ContainerTemplateSpec, error) {
	return s.containerRepo.ListContainerTemplateSpec(ctx)
}

// PageContainerTemplateSpec 分页查询共享运行配置。
func (s *containerService) PageContainerTemplateSpec(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageContainerTemplateSpec(ctx, pagination)
	if err != nil {
		return nil, err
	}
	return types.NewPageResult(total, pagination, items), nil
}

// CreateContainerTemplateDefinition 新增一条绑定行（只写定义，不创建/改动共享运行配置）。
// 校验：运行配置存在、镜像名非空、同一套配置下不重复绑定同一镜像
// （DB 唯一索引 uk_template_spec_image_name 兜底）。
func (s *containerService) CreateContainerTemplateDefinition(ctx context.Context, item *types.ContainerTemplateDefinition) error {
	if item == nil {
		return fmt.Errorf("container template definition is required")
	}
	if item.SpecID == 0 {
		return fmt.Errorf("container template spec id is required")
	}
	if _, err := s.containerRepo.GetContainerTemplateSpecByID(ctx, item.SpecID); err != nil {
		return err
	}
	item.ImageName = strings.TrimSpace(item.ImageName)
	if err := ensureImageName(item.ImageName); err != nil {
		return err
	}
	item.PullPolicy = normalizePullPolicy(item.PullPolicy)
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	if err := s.ensureImageNameNotBound(ctx, item.SpecID, item.ImageName); err != nil {
		return err
	}
	return s.containerRepo.CreateContainerTemplateDefinition(ctx, item)
}

func (s *containerService) GetContainerTemplateDefinitionByID(ctx context.Context, id int64) (*types.ContainerTemplateDefinition, error) {
	return s.containerRepo.GetContainerTemplateDefinitionByID(ctx, id)
}

// UpdateContainerTemplateDefinition 按主键更新绑定行（不动 created_at）。
// 只写绑定行本体（镜像名、拉取策略、展示名、描述、默认标记），不触碰共享运行配置
// ContainerTemplateSpec：命令 / 资源 / 端口等“怎么跑”的字段请走 UpdateContainerTemplateSpec。
// is_default=true 时额外走互斥的默认标记流程（先清空其它行）。
func (s *containerService) UpdateContainerTemplateDefinition(ctx context.Context, item *types.ContainerTemplateDefinition) error {
	if item == nil || item.ID == 0 {
		return fmt.Errorf("container template definition id is required")
	}
	item.ImageName = strings.TrimSpace(item.ImageName)
	if err := ensureImageName(item.ImageName); err != nil {
		return err
	}
	item.PullPolicy = normalizePullPolicy(item.PullPolicy)
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)

	existing, err := s.containerRepo.GetContainerTemplateDefinitionByID(ctx, item.ID)
	if err != nil {
		return err
	}
	// spec_id 是引用键：请求未携带时沿用库里已绑定的运行配置。
	if item.SpecID == 0 {
		item.SpecID = existing.SpecID
	} else if item.SpecID != existing.SpecID {
		if _, err := s.containerRepo.GetContainerTemplateSpecByID(ctx, item.SpecID); err != nil {
			return err
		}
	}
	// 同一套运行配置下不允许重复绑定同一镜像（与 CreateContainerTemplateDefinition 同一条不变量，
	// 更新时跳过自己所在的行）。
	bindings, err := s.containerRepo.ListContainerTemplateDefinitionBySpecID(ctx, item.SpecID)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.ID == item.ID {
			continue
		}
		if strings.TrimSpace(binding.ImageName) == item.ImageName {
			return fmt.Errorf("image %q is already bound to container template spec %d", item.ImageName, item.SpecID)
		}
	}

	return s.containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		if err := tx.UpdateContainerTemplateDefinition(ctx, item); err != nil {
			return err
		}
		// IsDefault 是全局互斥状态，整行更新不能表达“先清空其它行”，需要单独走默认标记流程。
		if item.IsDefault {
			return s.applyDefaultFlag(ctx, tx, item.ID)
		}
		return nil
	})
}

// upsertContainerTemplateSpec 按主键 upsert 共享运行配置：fallbackID 为绑定行里携带的引用 id。
// 优先用 item.ID，其次用 fallbackID；都为 0 时视为新建（由 BeforeCreate 分配新 id）。
// item 为 nil 时只做存在性校验，直接返回 fallbackID。
func (s *containerService) upsertContainerTemplateSpec(ctx context.Context, item *types.ContainerTemplateSpec, fallbackID int64) (int64, error) {
	if item == nil {
		if fallbackID == 0 {
			return 0, nil
		}
		spec, err := s.containerRepo.GetContainerTemplateSpecByID(ctx, fallbackID)
		if err != nil {
			return 0, err
		}
		return spec.ID, nil
	}

	spec := *item
	if spec.ID == 0 {
		spec.ID = fallbackID
	}

	if spec.ID != 0 {
		_, err := s.containerRepo.GetContainerTemplateSpecByID(ctx, spec.ID)
		switch {
		case err == nil:
			if err := s.containerRepo.UpdateContainerTemplateSpec(ctx, &spec); err != nil {
				return 0, err
			}
			return spec.ID, nil
		case stderrs.Is(err, gorm.ErrRecordNotFound):
			// 不存在，继续走新增（沿用导出文件里的主键，保持引用关系）
		default:
			return 0, err
		}
	}

	if err := s.containerRepo.CreateContainerTemplateSpec(ctx, &spec); err != nil {
		return 0, err
	}
	return spec.ID, nil
}

func (s *containerService) ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx context.Context, ownerType types.ContainerOwnerType, ownerIDs []int64) ([]*types.ContainerInstance, error) {
	return s.containerRepo.ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx, ownerType, ownerIDs)
}

func (s *containerService) DeleteContainerInstancesByOwnerTypeAndOwnerIDs(ctx context.Context, ownerType types.ContainerOwnerType, ownerIDs []int64) error {
	if len(ownerIDs) == 0 {
		return nil
	}
	instances, err := s.containerRepo.ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx, ownerType, ownerIDs)
	if err != nil {
		return err
	}

	var firstErr error
	for _, inst := range instances {
		if inst == nil || inst.ID == 0 {
			continue
		}
		if err := s.containerMgr.Delete(ctx, inst.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

func (s *containerService) PageContainerInstance(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageContainerInstance(ctx, pagination)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}

// func (s *containerService) PageContainerEvent(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error) {
// 	if pagination == nil {
// 		pagination = &types.Pagination{}
// 	}

// 	items, total, err := s.containerRepo.PageContainerEvent(ctx, pagination)
// 	if err != nil {
// 		return nil, err
// 	}

// 	return types.NewPageResult(total, pagination, items), nil
// }

func (s *containerService) PageOutboxEvent(ctx context.Context, pagination *types.Pagination) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageOutboxEvent(ctx, pagination)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
}
