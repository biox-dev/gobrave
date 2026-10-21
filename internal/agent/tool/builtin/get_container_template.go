package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetContainerTemplateToolName 是「按 @container:<id> 查看容器模板」的工具名。
const GetContainerTemplateToolName = "get_container_template"

// GetContainerTemplateInput 是 get_container_template 工具的入参。
//
// ContainerTemplateID 对应用户消息里的 @container:<id> 引用（如 "@container:2109711590457610240"），
// 它是 go_container_template_definition 主键，可直接作为 create_script /
// create_agent_analysis 的 container_template_id 使用。
type GetContainerTemplateInput struct {
	ContainerTemplateID string `json:"container_template_id"` // 容器模板 ID（支持 "@container:<id>" 形式）
}

// GetContainerTemplateOutput 是 get_container_template 工具返回给模型的结果。
//
// DisplayName / Description 原样取自绑定行 ContainerTemplateDefinition：
// DisplayName 是模板展示名，Description 是该模板（可运行配置 × 镜像）自身的说明。
type GetContainerTemplateOutput struct {
	ContainerTemplateID int64  `json:"container_template_id,string"`
	SpecID              int64  `json:"spec_id,string"`
	DisplayName         string `json:"display_name"`
	Description         string `json:"description"`
	ImageName           string `json:"image_name,omitempty"`
	PullPolicy          string `json:"pull_policy,omitempty"`
	IsDefault           bool   `json:"is_default"`
	// SpecName 是该绑定行关联的共享运行配置名称，便于区分「模板展示名」与「配置名」。
	SpecName string `json:"spec_name,omitempty"`
	Error    string `json:"error,omitempty"`
}

// GetContainerTemplate 返回一个「按 ID 查看容器模板」工具。
//
// 行为：
//   - 解析入参 container_template_id（容错剥离 @container: 前缀）；
//   - 按主键读取 go_container_template_definition（容器模板是全局资产，不按项目隔离）；
//   - 回传绑定行自身的 display_name / description，以及 image_name / pull_policy /
//     is_default / spec_name 等运行信息。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetContainerTemplate() tool.Tool {
	return tool.NewFunc(
		GetContainerTemplateToolName,
		"按 container_template_id 查看单个可运行容器模板（ContainerTemplateDefinition）。用于回答用户对 @container:<id> 引用的提问，也用于在创建脚本 / 分析节点前确认模板用途。返回 display_name（模板展示名）、description（模板自身的长描述）、image_name、pull_policy、is_default（是否系统默认模板）与 spec_name（关联的共享运行配置名）。该 ID 可原样作为 create_script / create_agent_analysis 的 container_template_id。不确定有哪些模板时先用 list_container_templates 列出全部模板。",
		tool.Schema("按 ID 查看容器模板的入参", map[string]any{
			"container_template_id": tool.StringProperty("容器模板 ID（go_container_template_definition 主键）。来自用户输入里的 @container:<id> 引用或 list_container_templates 的结果；字符串以保精度。"),
		}, "container_template_id"),
		func(ctx context.Context, in GetContainerTemplateInput) (GetContainerTemplateOutput, error) {
			containerTemplateID, err := parseContainerTemplateID(in.ContainerTemplateID)
			if err != nil {
				return GetContainerTemplateOutput{}, fmt.Errorf("%s: %w", GetContainerTemplateToolName, err)
			}
			if containerTemplateID <= 0 {
				return GetContainerTemplateOutput{}, fmt.Errorf("%s: container_template_id is required", GetContainerTemplateToolName)
			}

			browser := tool.ProjectResourceBrowserFrom(ctx)
			if browser == nil {
				return GetContainerTemplateOutput{}, fmt.Errorf("%s: project resource browser is not available in this runtime", GetContainerTemplateToolName)
			}

			detail, err := browser.GetContainerTemplateByID(ctx, containerTemplateID)
			if err != nil {
				return GetContainerTemplateOutput{
					ContainerTemplateID: containerTemplateID,
					Error:               err.Error(),
				}, nil
			}

			return GetContainerTemplateOutput{
				ContainerTemplateID: detail.DefinitionID,
				SpecID:              detail.SpecID,
				DisplayName:         detail.DisplayName,
				Description:         detail.Description,
				ImageName:           detail.ImageName,
				PullPolicy:          detail.PullPolicy,
				IsDefault:           detail.IsDefault,
				SpecName:            detail.SpecName,
			}, nil
		},
	).WithPermission("read", "", "按 ID 查看容器模板的展示名与描述")
}
