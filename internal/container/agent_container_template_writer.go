package container

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/types"
	"gorm.io/datatypes"
)

// 本文件实现容器模板写能力（builtin 的
// create/update_container_template_spec 与 create/update_container_template_definition）：
//
//   - 共享运行配置：go_container_template_spec（「怎么跑」：命令 / 资源 / 端口 / ENV / 挂载）；
//   - 绑定行：go_container_template_definition（运行配置 × 镜像，主键即对外 container_template_id）。
//
// 两层实体各对应一个窄接口（runtimecontext.ContainerTemplateSpecWriter /
// ContainerTemplateDefinitionWriter），由 projectResourceBrowser 一并实现，
// 在构造 builtin.Deps 时注入。业务校验（名称 / 镜像非空、镜像不重复绑定）统一交给
// interfaces.ContainerService，这里只做「工具入参 → 实体」的转换与读改写。

// defaultContainerTemplatePort 是未指定端口时使用的容器端口（与 DB 列默认值一致）。
const defaultContainerTemplatePort = 8787

// CreateContainerTemplateSpec 实现 runtimecontext.ContainerTemplateSpecWriter。
func (b *projectResourceBrowser) CreateContainerTemplateSpec(ctx context.Context, in runtimecontext.ContainerTemplateSpecCreateInput) (runtimecontext.ContainerTemplateSpecRecord, error) {
	if b.containerSvc == nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("create_container_template_spec: required services are not configured")
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("create_container_template_spec: name is required")
	}

	item := &types.ContainerTemplateSpec{
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		Command:     strings.TrimSpace(in.Command),
		CPU:         in.CPU,
		Memory:      in.Memory,
		Port:        in.Port,
		AppType:     strings.TrimSpace(in.AppType),
		ChangeUID:   in.ChangeUID,
	}
	if item.Port == 0 {
		// 与 go_container_template_spec.port 的默认值保持一致；工具层通常已经补过默认值，
		// 这里只是防止其它调用方绕过工具直接创建时落到 0。
		item.Port = defaultContainerTemplatePort
	}

	env, err := marshalJSONColumn(in.Env)
	if err != nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("create_container_template_spec: invalid env: %w", err)
	}
	mounts, err := marshalJSONColumn(in.Mounts)
	if err != nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("create_container_template_spec: invalid mounts: %w", err)
	}
	scheduling, err := marshalJSONColumn(in.SchedulingConstraint)
	if err != nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("create_container_template_spec: invalid scheduling_constraint: %w", err)
	}
	labels, err := marshalJSONColumn(in.Labels)
	if err != nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("create_container_template_spec: invalid labels: %w", err)
	}
	item.Env = env
	item.Mounts = mounts
	item.SchedulingConstraint = scheduling
	item.Labels = labels

	if err := b.containerSvc.CreateContainerTemplateSpec(ctx, item); err != nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("create_container_template_spec: failed to create: %w", err)
	}
	return toContainerTemplateSpecRecord(item), nil
}

// UpdateContainerTemplateSpec 实现 runtimecontext.ContainerTemplateSpecWriter。
//
// 读改写语义：先读出当前配置，只覆盖请求中给出的字段，再整体回写，
// 避免把未提交的字段写成零值。
func (b *projectResourceBrowser) UpdateContainerTemplateSpec(ctx context.Context, in runtimecontext.ContainerTemplateSpecUpdateInput) (runtimecontext.ContainerTemplateSpecRecord, error) {
	if b.containerSvc == nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("update_container_template_spec: required services are not configured")
	}
	if in.SpecID <= 0 {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("update_container_template_spec: container_template_spec_id is required")
	}

	existing, err := b.containerSvc.GetContainerTemplateSpecByID(ctx, in.SpecID)
	if err != nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("update_container_template_spec: failed to load spec %d: %w", in.SpecID, err)
	}
	if existing == nil || existing.ID == 0 {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("update_container_template_spec: container template spec %d not found", in.SpecID)
	}

	item := *existing
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("update_container_template_spec: name must not be empty")
		}
		item.Name = name
	}
	if in.Description != nil {
		item.Description = strings.TrimSpace(*in.Description)
	}
	if in.Command != nil {
		item.Command = strings.TrimSpace(*in.Command)
	}
	if in.CPU != nil {
		item.CPU = *in.CPU
	}
	if in.Memory != nil {
		item.Memory = *in.Memory
	}
	if in.Port != nil {
		item.Port = *in.Port
	}
	if in.AppType != nil {
		item.AppType = strings.TrimSpace(*in.AppType)
	}
	if in.ChangeUID != nil {
		item.ChangeUID = *in.ChangeUID
	}
	if in.Env != nil {
		env, err := marshalJSONColumn(in.Env)
		if err != nil {
			return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("update_container_template_spec: invalid env: %w", err)
		}
		item.Env = env
	}
	if in.Mounts != nil {
		mounts, err := marshalJSONColumn(in.Mounts)
		if err != nil {
			return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("update_container_template_spec: invalid mounts: %w", err)
		}
		item.Mounts = mounts
	}
	if in.SchedulingConstraint != nil {
		scheduling, err := marshalJSONColumn(in.SchedulingConstraint)
		if err != nil {
			return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("update_container_template_spec: invalid scheduling_constraint: %w", err)
		}
		item.SchedulingConstraint = scheduling
	}
	if in.Labels != nil {
		labels, err := marshalJSONColumn(in.Labels)
		if err != nil {
			return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("update_container_template_spec: invalid labels: %w", err)
		}
		item.Labels = labels
	}

	if err := b.containerSvc.UpdateContainerTemplateSpec(ctx, &item); err != nil {
		return runtimecontext.ContainerTemplateSpecRecord{}, fmt.Errorf("update_container_template_spec: failed to update spec %d: %w", in.SpecID, err)
	}

	// 回读一次：拿服务层归一化后的最终状态（与 UI 看到的一致）。
	if updated, err := b.containerSvc.GetContainerTemplateSpecByID(ctx, in.SpecID); err == nil && updated != nil {
		item = *updated
	}
	return toContainerTemplateSpecRecord(&item), nil
}

// CreateContainerTemplateDefinition 实现 runtimecontext.ContainerTemplateDefinitionWriter。
func (b *projectResourceBrowser) CreateContainerTemplateDefinition(ctx context.Context, in runtimecontext.ContainerTemplateDefinitionCreateInput) (runtimecontext.ContainerTemplateDefinitionRecord, error) {
	if b.containerSvc == nil {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, fmt.Errorf("create_container_template_definition: required services are not configured")
	}
	if in.SpecID <= 0 {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, fmt.Errorf("create_container_template_definition: container_template_spec_id is required")
	}
	imageName := strings.TrimSpace(in.ImageName)
	if imageName == "" {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, fmt.Errorf("create_container_template_definition: image_name is required")
	}

	item := &types.ContainerTemplateDefinition{
		SpecID:      in.SpecID,
		ImageName:   imageName,
		PullPolicy:  strings.TrimSpace(in.PullPolicy),
		Name:        strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description),
	}
	if err := b.containerSvc.CreateContainerTemplateDefinition(ctx, item); err != nil {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, fmt.Errorf("create_container_template_definition: failed to create: %w", err)
	}
	return toContainerTemplateDefinitionRecord(item), nil
}

// UpdateContainerTemplateDefinition 实现 runtimecontext.ContainerTemplateDefinitionWriter。
//
// 读改写语义与 UpdateContainerTemplateSpec 相同：未给出的字段沿用库里的原值。
func (b *projectResourceBrowser) UpdateContainerTemplateDefinition(ctx context.Context, in runtimecontext.ContainerTemplateDefinitionUpdateInput) (runtimecontext.ContainerTemplateDefinitionRecord, error) {
	if b.containerSvc == nil {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, fmt.Errorf("update_container_template_definition: required services are not configured")
	}
	if in.DefinitionID <= 0 {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, fmt.Errorf("update_container_template_definition: container_template_definition_id is required")
	}

	existing, err := b.containerSvc.GetContainerTemplateDefinitionByID(ctx, in.DefinitionID)
	if err != nil {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, fmt.Errorf("update_container_template_definition: failed to load definition %d: %w", in.DefinitionID, err)
	}
	if existing == nil || existing.ID == 0 {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, fmt.Errorf("update_container_template_definition: container template definition %d not found", in.DefinitionID)
	}

	item := *existing
	if in.SpecID != nil {
		item.SpecID = *in.SpecID
	}
	if in.ImageName != nil {
		imageName := strings.TrimSpace(*in.ImageName)
		if imageName == "" {
			return runtimecontext.ContainerTemplateDefinitionRecord{}, fmt.Errorf("update_container_template_definition: image_name must not be empty")
		}
		item.ImageName = imageName
	}
	if in.PullPolicy != nil {
		item.PullPolicy = strings.TrimSpace(*in.PullPolicy)
	}
	if in.Name != nil {
		item.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		item.Description = strings.TrimSpace(*in.Description)
	}

	if err := b.containerSvc.UpdateContainerTemplateDefinition(ctx, &item); err != nil {
		return runtimecontext.ContainerTemplateDefinitionRecord{}, fmt.Errorf("update_container_template_definition: failed to update definition %d: %w", in.DefinitionID, err)
	}

	if updated, err := b.containerSvc.GetContainerTemplateDefinitionByID(ctx, in.DefinitionID); err == nil && updated != nil {
		item = *updated
	}
	return toContainerTemplateDefinitionRecord(&item), nil
}

// marshalJSONColumn 把工具入参里的 JSON 结构序列化进 datatypes.JSON 列。
// nil / 空值返回 nil（落库为 NULL）。
func marshalJSONColumn(value any) (datatypes.JSON, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" || string(raw) == "{}" || string(raw) == "[]" {
		return nil, nil
	}
	return datatypes.JSON(raw), nil
}

// toContainerTemplateSpecRecord 把实体转换为工具回传记录。
func toContainerTemplateSpecRecord(item *types.ContainerTemplateSpec) runtimecontext.ContainerTemplateSpecRecord {
	if item == nil {
		return runtimecontext.ContainerTemplateSpecRecord{}
	}
	return runtimecontext.ContainerTemplateSpecRecord{
		SpecID:               item.ID,
		Name:                 item.Name,
		Description:          item.Description,
		Command:              item.Command,
		CPU:                  item.CPU,
		Memory:               item.Memory,
		Port:                 item.Port,
		AppType:              item.AppType,
		Env:                  jsonObjectColumn(item.Env),
		Mounts:               jsonObjectListColumn(item.Mounts),
		SchedulingConstraint: jsonObjectColumn(item.SchedulingConstraint),
		Labels:               jsonObjectColumn(item.Labels),
		ChangeUID:            item.ChangeUID,
	}
}

// toContainerTemplateDefinitionRecord 把绑定行实体转换为工具回传记录。
func toContainerTemplateDefinitionRecord(item *types.ContainerTemplateDefinition) runtimecontext.ContainerTemplateDefinitionRecord {
	if item == nil {
		return runtimecontext.ContainerTemplateDefinitionRecord{}
	}
	return runtimecontext.ContainerTemplateDefinitionRecord{
		DefinitionID: item.ID,
		SpecID:       item.SpecID,
		ImageName:    item.ImageName,
		PullPolicy:   item.PullPolicy,
		Name:         item.Name,
		Description:  item.Description,
	}
}

// jsonObjectColumn 把 datatypes.JSON 解析为对象（解析失败 / 空值时返回 nil）。
func jsonObjectColumn(raw datatypes.JSON) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// jsonObjectListColumn 把 datatypes.JSON 解析为对象数组（解析失败 / 空值时返回 nil）。
func jsonObjectListColumn(raw datatypes.JSON) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := make([]map[string]any, 0)
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
