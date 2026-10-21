package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// ListContainerTemplatesToolName 是「列出全部容器模板」工具名。
const ListContainerTemplatesToolName = "list_container_templates"

// ListContainerTemplatesInput 是 list_container_templates 工具的入参。
//
// 容器模板是全局资产（不属于某个项目），因此不需要任何参数；模型直接调用即可。
type ListContainerTemplatesInput struct{}

// ContainerTemplateItem 是单个容器模板的摘要（工具回传给模型的字段）。
//
// ID 是 go_container_template_definition 主键（即对外 ContainerTemplate.ID），
// 可原样作为 script.container_template_id 等引用字段使用；Name / Description
// 来自该绑定行关联的 go_container_template_spec。
type ContainerTemplateItem struct {
	ID          int64  `json:"id,string"`
	SpecID      int64  `json:"spec_id,string"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ListContainerTemplatesOutput 是 list_container_templates 工具返回给模型的结果。
type ListContainerTemplatesOutput struct {
	Total     int                     `json:"total"`
	Templates []ContainerTemplateItem `json:"container_templates"`
	Message   string                  `json:"message,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

// ListContainerTemplates 返回一个「列出全部容器模板」工具。
//
// 行为：
//   - 通过注入的 ProjectResourceBrowser 列出全部 go_container_template_definition；
//   - 每行回传 definition 的 ID 及关联 ContainerTemplateSpec 的 Name / Description。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func ListContainerTemplates() tool.Tool {
	return tool.NewFunc(
		ListContainerTemplatesToolName,
		"列出系统中全部可运行的容器模板（ContainerTemplateDefinition）。返回每个模板的 id（字符串形式，避免大整数精度丢失，即绑定行主键，可直接用于 container_template_id 引用）、spec_id（关联的共享运行配置 ID）、name（ContainerTemplateSpec 的名称）与 description（ContainerTemplateSpec 的描述）。容器模板是全局资产，不需要传入项目或分页参数。已知具体模板 ID（如用户消息里的 @container:<id>）时，改用 get_container_template 查看该模板自身的 display_name 与 description。",
		tool.Schema("列出全部容器模板的入参（无需参数）", map[string]any{}),
		func(ctx context.Context, _ ListContainerTemplatesInput) (ListContainerTemplatesOutput, error) {
			browser := tool.ProjectResourceBrowserFrom(ctx)
			if browser == nil {
				return ListContainerTemplatesOutput{}, fmt.Errorf("%s: project resource browser is not available in this runtime", ListContainerTemplatesToolName)
			}

			templates, err := browser.ListContainerTemplates(ctx)
			if err != nil {
				return ListContainerTemplatesOutput{
					Templates: []ContainerTemplateItem{},
					Error:     err.Error(),
				}, nil
			}

			out := ListContainerTemplatesOutput{
				Total:     len(templates),
				Templates: make([]ContainerTemplateItem, 0, len(templates)),
			}
			for _, tpl := range templates {
				out.Templates = append(out.Templates, ContainerTemplateItem{
					ID:          tpl.DefinitionID,
					SpecID:      tpl.SpecID,
					Name:        tpl.Name,
					Description: tpl.Description,
				})
			}
			if out.Total == 0 {
				out.Message = "系统中还没有容器模板。"
			}
			return out, nil
		},
	).WithPermission("read", "", "列出全部容器模板")
}
