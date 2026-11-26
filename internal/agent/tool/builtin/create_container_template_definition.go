package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateContainerTemplateDefinitionToolName 是「创建容器模板绑定行」工具名。
const CreateContainerTemplateDefinitionToolName = "create_container_template_definition"

// CreateContainerTemplateDefinitionInput 是 create_container_template_definition 工具的入参。
//
// 绑定行 = 共享运行配置 × 一个具体镜像；一行即一个可直接运行的容器模板，
// 其主键就是对外暴露的 container_template_id。
type CreateContainerTemplateDefinitionInput struct {
	// SpecID 是共享运行配置主键，必须已存在（先用 create_container_template_spec 创建）。
	SpecID string `json:"spec_id"`
	// ImageName 是完整镜像名（含 registry / namespace / tag），必填。
	ImageName  string `json:"image_name"`
	PullPolicy string `json:"pull_policy"`
	// Name / Description 是该可运行模板自身的展示名与描述（与运行配置的 name 相互独立）。
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CreateContainerTemplateDefinition 返回「创建容器模板绑定行」工具。
//
// 行为：
//   - spec_id 必填且必须指向已存在的共享运行配置；
//   - image_name 必填；同一运行配置下不允许重复绑定同一镜像（由服务层校验）；
//   - pull_policy 取 Always / IfNotPresent / Never，空值按 IfNotPresent 处理；
//   - 回传绑定行主键（即对外 container_template_id）与镜像信息。
//
// 该工具会创建业务数据（写库），因此声明 write 授权：上层在执行前会先走权限门禁。
func CreateContainerTemplateDefinition(writer runtimecontext.ContainerTemplateDefinitionWriter) tool.Tool {
	return tool.NewFunc(
		CreateContainerTemplateDefinitionToolName,
		"Create a runnable container template: bind an existing shared run configuration (spec_id, created by create_container_template_spec) to a concrete image (image_name) and set pull_policy / display name / description. The returned definition_id IS the container_template_id used by create_script / create_analysis_node_agent. The same spec can be bound to several different images.",
		tool.Schema("创建容器模板绑定行的入参", map[string]any{
			"spec_id":     tool.StringProperty("共享运行配置 ID（必填，go_container_template_spec 主键，字符串以保精度）。先用 create_container_template_spec 或 list_container_templates 得到。"),
			"image_name":  tool.StringProperty("完整镜像名（必填，含 registry / namespace / tag），如 docker.io/rocker/rstudio:4.4。"),
			"pull_policy": tool.StringProperty("镜像拉取策略：Always / IfNotPresent / Never（默认 IfNotPresent）。"),
			"name":        tool.StringProperty("该容器模板的展示名（可选）；与运行配置的 name 相互独立。"),
			"description": tool.StringProperty("该容器模板的说明（可选），用于说明它做什么 / 适用什么场景。"),
		}, "spec_id", "image_name"),
		func(ctx context.Context, in CreateContainerTemplateDefinitionInput) (ContainerTemplateDefinitionWriteOutput, error) {
			specID, err := parseContainerTemplateSpecID(in.SpecID)
			if err != nil {
				return ContainerTemplateDefinitionWriteOutput{}, wrapToolNameError(CreateContainerTemplateDefinitionToolName, err)
			}
			imageName := strings.TrimSpace(in.ImageName)
			if imageName == "" {
				return ContainerTemplateDefinitionWriteOutput{}, fmt.Errorf("%s: image_name is required", CreateContainerTemplateDefinitionToolName)
			}
			if writer == nil {
				return ContainerTemplateDefinitionWriteOutput{}, fmt.Errorf("%s: container template definition writer is not available in this runtime", CreateContainerTemplateDefinitionToolName)
			}

			record, createErr := writer.CreateContainerTemplateDefinition(ctx, runtimecontext.ContainerTemplateDefinitionCreateInput{
				SpecID:      specID,
				ImageName:   imageName,
				PullPolicy:  strings.TrimSpace(in.PullPolicy),
				Name:        strings.TrimSpace(in.Name),
				Description: strings.TrimSpace(in.Description),
			})
			if createErr != nil {
				return ContainerTemplateDefinitionWriteOutput{
					ContainerTemplateDefinitionRecord: runtimecontext.ContainerTemplateDefinitionRecord{
						SpecID:    specID,
						ImageName: imageName,
					},
					Error: createErr.Error(),
				}, nil
			}
			return ContainerTemplateDefinitionWriteOutput{ContainerTemplateDefinitionRecord: record}, nil
		},
	).WithPermission("write", "", "创建容器模板绑定行（运行配置 × 镜像）")
}
