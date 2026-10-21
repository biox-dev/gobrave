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

// containerTemplateSpecFromReadModel 从对外模板抽出"怎么跑"的字段，用于写入 go_container_template_spec。
func containerTemplateSpecFromReadModel(tpl *types.ContainerTemplate) *types.ContainerTemplateSpec {
	return &types.ContainerTemplateSpec{
		ID:                   tpl.SpecID,
		Name:                 tpl.Name,
		Description:          tpl.Description,
		Command:              tpl.Command,
		CPU:                  tpl.CPU,
		Memory:               tpl.Memory,
		WorkDir:              tpl.WorkDir,
		Port:                 tpl.Port,
		AppType:              tpl.AppType,
		Env:                  tpl.Env,
		Mounts:               tpl.Mounts,
		SchedulingConstraint: tpl.SchedulingConstraint,
		Labels:               tpl.Labels,
		ChangeUID:            tpl.ChangeUID,
	}
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

// containerTemplateDefinitionFromReadModel 从对外模板抽出绑定行字段。
// 只搬 DisplayName / DefinitionDescription：读模型里的 Name / Description 可能是配置名的回退值，
// 直接写入会把配置信息误固化成绑定行内容，所以这两个字段必须由请求显式携带。
func containerTemplateDefinitionFromReadModel(tpl *types.ContainerTemplate) *types.ContainerTemplateDefinition {
	return &types.ContainerTemplateDefinition{
		ID:          tpl.ID,
		SpecID:      tpl.SpecID,
		ImageName:   strings.TrimSpace(tpl.ImageName),
		PullPolicy:  normalizePullPolicy(tpl.PullPolicy),
		DisplayName: strings.TrimSpace(tpl.DisplayName),
		Description: strings.TrimSpace(tpl.DefinitionDescription),
	}
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
	// 先读出整行再置位：绑定行的更新是整行语义（display_name 支持显式清空），
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

// createContainerTemplate 写入"共享配置 + 镜像绑定"，并回填 tpl.ID / tpl.SpecID。
//
//	tpl.SpecID != 0：复用已有配置，只在它下面新增一个镜像绑定（同一套运行配置挂多个镜像）。
//	tpl.SpecID == 0：用 tpl 里的运行配置字段新建配置，再建第一个绑定。
func (s *containerService) createContainerTemplate(ctx context.Context, tpl *types.ContainerTemplate) error {
	definition := containerTemplateDefinitionFromReadModel(tpl)

	if tpl.SpecID != 0 {
		spec, err := s.containerRepo.GetContainerTemplateSpecByID(ctx, tpl.SpecID)
		if err != nil {
			return err
		}
		if err := s.ensureImageNameNotBound(ctx, tpl.SpecID, definition.ImageName); err != nil {
			return err
		}
		if err := s.containerRepo.CreateContainerTemplateDefinition(ctx, definition); err != nil {
			return err
		}

		tpl.ID = definition.ID
		tpl.Name = spec.Name
		if definition.DisplayName != "" {
			tpl.Name = definition.DisplayName
		}
		return s.applyDefaultFlag(ctx, s.containerRepo, definition.ID)
	}

	spec := containerTemplateSpecFromReadModel(tpl)

	err := s.containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		if err := tx.CreateContainerTemplateSpec(ctx, spec); err != nil {
			return err
		}
		definition.SpecID = spec.ID
		if err := tx.CreateContainerTemplateDefinition(ctx, definition); err != nil {
			return err
		}
		return s.applyDefaultFlag(ctx, tx, definition.ID)
	})
	if err != nil {
		return err
	}

	tpl.ID = definition.ID
	tpl.SpecID = spec.ID
	return nil
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

// updateContainerTemplate 更新共享配置（按绑定行指向的 spec）与该绑定行本身。
func (s *containerService) updateContainerTemplate(ctx context.Context, tpl *types.ContainerTemplate) error {
	binding, err := s.containerRepo.GetContainerTemplateDefinitionByID(ctx, tpl.ID)
	if err != nil {
		return err
	}

	spec := containerTemplateSpecFromReadModel(tpl)
	spec.ID = binding.SpecID
	// display_name 归绑定行所有：请求没带 name 时不要用空值覆盖共享配置名
	// （UpdateContainerTemplateSpec 会跳过零值，这里保持 Name 默认为空即可）。
	definition := containerTemplateDefinitionFromReadModel(tpl)
	definition.ID = binding.ID
	definition.SpecID = binding.SpecID

	return s.containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		if err := tx.UpdateContainerTemplateSpec(ctx, spec); err != nil {
			return err
		}
		if err := tx.UpdateContainerTemplateDefinition(ctx, definition); err != nil {
			return err
		}
		// IsDefault 不在上面的通用更新里处理（它是全局互斥状态，需要先清空其它行）；
		// 只有请求显式要求「这个模板为默认」时才走 set-default 流程。
		if tpl.IsDefault {
			return s.applyDefaultFlag(ctx, tx, binding.ID)
		}
		return nil
	})
}

func (s *containerService) CreateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error {
	if item == nil {
		return fmt.Errorf("container template is required")
	}
	if err := ensureImageName(item.ImageName); err != nil {
		return err
	}
	return s.createContainerTemplate(ctx, item)
}

func (s *containerService) GetContainerTemplateByID(ctx context.Context, id int64) (*types.ContainerTemplate, error) {
	return s.containerRepo.GetContainerTemplateByID(ctx, id)
}

func (s *containerService) UpdateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error {
	if item == nil || item.ID == 0 {
		return fmt.Errorf("container template id is required")
	}
	if err := ensureImageName(item.ImageName); err != nil {
		return err
	}
	return s.updateContainerTemplate(ctx, item)
}

// ensureImageName 校验绑定行的镜像名非空（镜像名直接存在绑定行上）。
func ensureImageName(imageName string) error {
	if strings.TrimSpace(imageName) == "" {
		return fmt.Errorf("image name is required")
	}
	return nil
}

func (s *containerService) DeleteContainerTemplate(ctx context.Context, id int64) error {
	binding, err := s.containerRepo.GetContainerTemplateDefinitionByID(ctx, id)
	if err != nil {
		return err
	}

	return s.containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		if err := tx.DeleteContainerTemplateDefinition(ctx, binding.ID); err != nil {
			return err
		}
		// 该套配置已无任何镜像绑定时顺带清理，避免留下孤儿配置行。
		// remain, err := tx.ListContainerTemplateDefinitionBySpecID(ctx, binding.SpecID)
		// if err != nil {
		// 	return err
		// }
		// if len(remain) == 0 {
		// 	return tx.DeleteContainerTemplateSpec(ctx, binding.SpecID)
		// }
		return nil
	})
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
// 需要强制删除时先删掉这些绑定行（DeleteContainerTemplate 会在最后一行绑定删除后顺带清理配置）。
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

// CreateContainerTemplateDefinition 新增一条绑定行。
// 绑定行带镜像名与拉取策略，因此这里校验镜像名非空、且同一套配置下不重复绑定同一镜像
// （与 CreateContainerTemplate 走同一条不变量，DB 唯一索引 uk_template_spec_image_name 兜底）。
func (s *containerService) CreateContainerTemplateDefinition(ctx context.Context, item *types.ContainerTemplateDefinition) error {
	if item == nil {
		return fmt.Errorf("container template definition is required")
	}
	if item.SpecID == 0 {
		return fmt.Errorf("container template spec id is required")
	}
	item.ImageName = strings.TrimSpace(item.ImageName)
	if err := ensureImageName(item.ImageName); err != nil {
		return err
	}
	item.PullPolicy = normalizePullPolicy(item.PullPolicy)
	if err := s.ensureImageNameNotBound(ctx, item.SpecID, item.ImageName); err != nil {
		return err
	}
	return s.containerRepo.CreateContainerTemplateDefinition(ctx, item)
}

func (s *containerService) GetContainerTemplateDefinitionByID(ctx context.Context, id int64) (*types.ContainerTemplateDefinition, error) {
	return s.containerRepo.GetContainerTemplateDefinitionByID(ctx, id)
}

// UpdateContainerTemplateDefinition 按主键更新绑定行（不动 created_at）。
// 不校验 (spec_id, image_name) 唯一性：更新的是同一行，唯一索引不会冲突。
func (s *containerService) UpdateContainerTemplateDefinition(ctx context.Context, item *types.ContainerTemplateDefinition) error {
	if item == nil || item.ID == 0 {
		return fmt.Errorf("container template definition id is required")
	}
	item.ImageName = strings.TrimSpace(item.ImageName)
	if err := ensureImageName(item.ImageName); err != nil {
		return err
	}
	item.PullPolicy = normalizePullPolicy(item.PullPolicy)
	return s.containerRepo.UpdateContainerTemplateDefinition(ctx, item)
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

func (s *containerService) CreateAppSessionByTemplate(ctx context.Context, userID int64, projectID int64, containerTemplateID int64, name string) (*types.AppSession, error) {
	return s.createAppSessionByTemplate(ctx, userID, projectID, containerTemplateID, name, 0, "")
}

func (s *containerService) CreateAppSessionByTemplateForAnalysisNode(ctx context.Context, userID int64, projectID int64, containerTemplateID int64, name string, analysisNodeID int64, workspacePath string) (*types.AppSession, error) {
	return s.createAppSessionByTemplate(ctx, userID, projectID, containerTemplateID, name, analysisNodeID, workspacePath)
}

func (s *containerService) createAppSessionByTemplate(ctx context.Context, userID int64, projectID int64, containerTemplateID int64, name string, analysisNodeID int64, workspacePath string) (*types.AppSession, error) {
	if userID == 0 {
		return nil, fmt.Errorf("user id is required")
	}
	if projectID == 0 {
		return nil, fmt.Errorf("project id is required")
	}
	if containerTemplateID == 0 {
		return nil, fmt.Errorf("container template id is required")
	}

	tpl, err := s.containerRepo.GetContainerTemplateByID(ctx, containerTemplateID)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("app-session-%d-%d", projectID, containerTemplateID)
		if strings.TrimSpace(tpl.Name) != "" {
			name = fmt.Sprintf("%s-%d", strings.TrimSpace(tpl.Name), projectID)
		}
	}

	session := &types.AppSession{
		UserID:              userID,
		ProjectID:           projectID,
		AnalysisNodeID:      analysisNodeID,
		ContainerTemplateID: containerTemplateID,
		Name:                name,
		// AppType:             tpl.AppType,
		Status:        "CREATE_PENDING", //create_pending PENDING_CREATION
		WorkspacePath: strings.TrimSpace(workspacePath),
	}
	if err := s.containerRepo.CreateAppSession(ctx, session); err != nil {
		return nil, err
	}

	_, err = s.containerMgr.CreateByTemplate(ctx, containerTemplateID, types.ContainerOwnerAppSession, session.ID, name)
	if err != nil {
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return nil, err
	}

	return session, nil
}

func (s *containerService) StartAppSession(ctx context.Context, userID int64, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}
	queueStatus, err := s.containerMgr.QueueStatus(ctx)
	if err != nil {
		return err
	}
	if queueStatus != nil && queueStatus.MaxPending > 0 && queueStatus.PendingCount >= int64(queueStatus.MaxPending) {
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return fmt.Errorf("container start queue is full (%d/%d pending), please try again later",
			queueStatus.PendingCount, queueStatus.MaxPending)
	}

	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil {
		return err
	}

	if err := s.containerMgr.Start(ctx, inst.ID); err != nil {
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return err
	}

	session.Status = "START_PENDING"
	session.StoppedAt = nil
	return s.containerRepo.UpdateAppSession(ctx, session)
}

func (s *containerService) StopAppSession(ctx context.Context, userID int64, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}
	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil {
		return err
	}

	if err := s.containerMgr.Stop(ctx, inst.ID); err != nil {
		if err == types.ErrContainerAlreadyStopped {
			session.Status = "STOPPED"
			return s.containerRepo.UpdateAppSession(ctx, session)
		}
		session.Status = "FAILED"
		_ = s.containerRepo.UpdateAppSession(ctx, session)
		return err
	}

	// now := time.Now()
	// session.Status = "STOPPED"
	// session.StoppedAt = &now
	// s.containerRepo.UpdateAppSession(ctx, session)
	return nil
}

func (s *containerService) DeleteAppSession(ctx context.Context, userID int64, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}

	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil && !stderrs.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		if err := s.containerMgr.Delete(ctx, inst.ID); err != nil {
			return err
		}
	}

	return s.containerRepo.DeleteAppSession(ctx, session.ID)
}

func (s *containerService) GetAppSessionByID(ctx context.Context, userID int64, appSessionID int64) (*types.AppSession, error) {
	return s.ensureOwnedAppSession(ctx, userID, appSessionID)
}

func (s *containerService) ListAppSessionByUserID(ctx context.Context, userID int64) ([]*types.AppSession, error) {
	items, err := s.containerRepo.ListAppSession(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]*types.AppSession, 0)
	for _, item := range items {
		if item.UserID == userID {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func (s *containerService) PageAppSessionByUserID(ctx context.Context, userID int64, pagination *types.Pagination, query *types.AppSessionPageQuery) (*types.PageResult, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items, total, err := s.containerRepo.PageAppSessionByUserID(ctx, userID, pagination, query)
	if err != nil {
		return nil, err
	}

	return types.NewPageResult(total, pagination, items), nil
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

func (s *containerService) ensureOwnedAppSession(ctx context.Context, userID int64, appSessionID int64) (*types.AppSession, error) {
	session, err := s.containerRepo.GetAppSessionByID(ctx, appSessionID)
	if err != nil {
		return nil, err
	}
	if session.UserID != userID {
		return nil, gorm.ErrRecordNotFound
	}
	return session, nil
}
func (s *containerService) RecreateAppSessionContainer(ctx context.Context, userID int64, appSessionID int64) error {
	session, err := s.ensureOwnedAppSession(ctx, userID, appSessionID)
	if err != nil {
		return err
	}

	// payload, err := json.Marshal(map[string]any{
	// 	"app_session_id": appSessionID,
	// 	"user_id":        strings.TrimSpace(userID),
	// })
	if err != nil {
		return err
	}

	// if err := s.containerRepo.CreateOutboxEvent(ctx, &types.OutboxEvent{
	// 	Type:    manager.OutboxEventTypeAppSessionRecreateRequest,
	// 	Payload: payload,
	// 	Status:  "pending",
	// }); err != nil {
	// 	return err
	// }

	session.Status = "RECREATE_PENDING"
	session.StoppedAt = nil
	if err := s.containerRepo.UpdateAppSession(ctx, session); err != nil {
		return err
	}
	inst, err := s.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerAppSession, session.ID)
	if err != nil {
		return err
	}
	if err := s.containerMgr.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerReCreatePending, manager.OutboxEventTypeRecreateRequest); err != nil {
		return err
	}

	return nil
}
