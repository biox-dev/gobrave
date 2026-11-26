package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateContainerTemplateDefinitionToolName 是「更新容器模板绑定行」工具名。
const UpdateContainerTemplateDefinitionToolName = "update_container_template_definition"

// UpdateContainerTemplateDefinitionInput 是 update_container_template_definition 工具的入参。
//
// 指针字段为 nil 表示不修改；definition_id 必填。该工具只改绑定行本体
// （镜像名 / 拉取策略 / 展示名 / 描述 / 关联的 spec_id），命令与资源等「怎么跑」
// 的字段请改用 update_container_template_spec。
type UpdateContainerTemplateDefinitionInput struct {
	DefinitionID string  `json:"definition_id"`
	SpecID       string  `json:"spec_id"`
	ImageName    *string `json:"image_name"`
	PullPolicy   *string `json:"pull_policy"`
	Name         *string `json:"name"`
	Description  *string `json:"description"`
}

// UpdateContainerTemplateDefinition 返回「更新容器模板绑定行」工具。
//
// 行为：
//   - definition_id 必填（兼容 @container:<id> 引用）；
//   - 只覆盖本次给出的字段，未给出的字段保持数据库原值；
//   - spec_id 非空时把绑定行改挂到另一个已存在的共享运行配置；
//   - 回传更新后的记录（definition_id 即对外 container_template_id）。
//
// 该工具会修改业务数据（写库），因此声明 write 授权：上层在执行前会先走权限门禁。
func UpdateContainerTemplateDefinition(writer runtimecontext.ContainerTemplateDefinitionWriter) tool.Tool {
	return tool.NewFunc(
		UpdateContainerTemplateDefinitionToolName,
		"Update a container template's binding row (ContainerTemplateDefinition) by definition_id: change image_name / pull_policy / display name / description, or re-bind it to another shared run configuration (spec_id). Only fields you pass are changed. Command / CPU / memory / port / env live on the shared spec — use update_container_template_spec for those. The definition_id is the same container_template_id referenced by scripts and analysis nodes.",
		tool.Schema("更新容器模板绑定行的入参", map[string]any{
			"definition_id": tool.StringProperty("绑定行 ID（必填，即对外 container_template_id / ContainerTemplateDefinition.ID，字符串以保精度；兼容 @container:<id> 引用）。"),
			"spec_id":       tool.StringProperty("新的共享运行配置 ID；不传表示不修改（改挂配置时必须是已存在的 spec）。"),
			"image_name":    tool.StringProperty("新的完整镜像名；不传表示不修改。"),
			"pull_policy":   tool.StringProperty("新的镜像拉取策略：Always / IfNotPresent / Never；不传表示不修改。"),
			"name":          tool.StringProperty("新的模板展示名；不传表示不修改。"),
			"description":   tool.StringProperty("新的模板说明；不传表示不修改（传空串可清空描述）。"),
		}, "definition_id"),
		func(ctx context.Context, in UpdateContainerTemplateDefinitionInput) (ContainerTemplateDefinitionWriteOutput, error) {
			definitionID, err := parseContainerTemplateDefinitionID(in.DefinitionID)
			if err != nil {
				return ContainerTemplateDefinitionWriteOutput{}, wrapToolNameError(UpdateContainerTemplateDefinitionToolName, err)
			}

			update := runtimecontext.ContainerTemplateDefinitionUpdateInput{
				DefinitionID: definitionID,
				ImageName:    trimStringPointer(in.ImageName),
				PullPolicy:   trimStringPointer(in.PullPolicy),
				Name:         trimStringPointer(in.Name),
				Description:  trimStringPointer(in.Description),
			}

			specIDRaw := strings.TrimSpace(in.SpecID)
			if specIDRaw != "" {
				specID, specErr := parseContainerTemplateSpecID(specIDRaw)
				if specErr != nil {
					return ContainerTemplateDefinitionWriteOutput{}, wrapToolNameError(UpdateContainerTemplateDefinitionToolName, specErr)
				}
				update.SpecID = &specID
			}

			if !hasContainerTemplateDefinitionUpdate(update) {
				return ContainerTemplateDefinitionWriteOutput{}, fmt.Errorf("%s: at least one field to update must be provided", UpdateContainerTemplateDefinitionToolName)
			}
			if writer == nil {
				return ContainerTemplateDefinitionWriteOutput{}, fmt.Errorf("%s: container template definition writer is not available in this runtime", UpdateContainerTemplateDefinitionToolName)
			}

			record, updateErr := writer.UpdateContainerTemplateDefinition(ctx, update)
			if updateErr != nil {
				return ContainerTemplateDefinitionWriteOutput{
					ContainerTemplateDefinitionRecord: runtimecontext.ContainerTemplateDefinitionRecord{DefinitionID: definitionID},
					Error:                             updateErr.Error(),
				}, nil
			}
			return ContainerTemplateDefinitionWriteOutput{ContainerTemplateDefinitionRecord: record}, nil
		},
	).WithPermission("write", "", "更新容器模板绑定行（运行配置 × 镜像）")
}

// hasContainerTemplateDefinitionUpdate 判断更新入参是否至少给出一个字段。
func hasContainerTemplateDefinitionUpdate(in runtimecontext.ContainerTemplateDefinitionUpdateInput) bool {
	return in.SpecID != nil ||
		in.ImageName != nil ||
		in.PullPolicy != nil ||
		in.Name != nil ||
		in.Description != nil
}
