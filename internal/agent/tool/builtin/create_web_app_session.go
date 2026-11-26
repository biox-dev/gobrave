package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateWebAppSessionToolName 是「按容器模板创建 Web 应用会话」的工具名。
const CreateWebAppSessionToolName = "create_web_app_session"

// CreateWebAppSessionInput 是 create_web_app_session 工具的入参。
//
// UserID / ProjectID 不由模型提供：它们取自执行事实（tool.UserIDFrom / tool.ProjectIDFrom）。
type CreateWebAppSessionInput struct {
	// ContainerTemplateID 是可运行的容器模板 ID（支持 "@container:<id>"）。
	ContainerTemplateID string `json:"container_template_id"`
	// Name 是会话展示名（可选）；为空时由服务端按模板生成默认名。
	Name string `json:"name"`
}

// CreateAppSessionOutput 是创建应用会话工具（Web / 终端）返回给模型的结果。
type CreateAppSessionOutput struct {
	runtimecontext.AppSessionCreateResult
	Error string `json:"error,omitempty"`
}

// CreateWebAppSession 返回「按容器模板创建 Web 应用会话」的工具。
//
// 与终端会话的区别：容器按模板命令启动并暴露服务，前端经网关访问该应用
// （SessionKind=web），不做交互式 shell。该工具只创建会话，容器的实际创建由
// 服务层按模板异步完成。
//
// 该工具会创建业务数据并拉起容器，因此声明 write 授权：上层在执行前会先走权限门禁。
func CreateWebAppSession(creator runtimecontext.AppSessionCreator) tool.Tool {
	return tool.NewFunc(
		CreateWebAppSessionToolName,
		"按容器模板创建一条 Web 应用会话（SessionKind=web）：容器按模板命令启动并暴露服务，前端经网关访问该应用。适用于「把某个容器模板作为 Web 应用跑起来」的场景。只创建会话，不执行命令；若需要交互式 shell 请改用 create_terminal_app_session。返回 session_id / name / status。用户与项目取自当前会话上下文，无需传入。不确定有哪些模板时先调用 list_container_templates / get_container_template。",
		tool.Schema("创建 Web 应用会话的入参", map[string]any{
			"container_template_id": tool.StringProperty("可运行的容器模板 ID（必填，字符串以保精度）；可直接使用用户消息里的 @container:<id>（冒号后的 ID）。"),
			"name":                  tool.StringProperty("会话展示名（可选）；为空时由服务端按模板生成默认名。"),
		}, "container_template_id"),
		func(ctx context.Context, in CreateWebAppSessionInput) (CreateAppSessionOutput, error) {
			userID := tool.UserIDFrom(ctx)
			projectID := tool.ProjectIDFrom(ctx)
			if projectID <= 0 {
				return CreateAppSessionOutput{}, fmt.Errorf("create_web_app_session: project_id is required (no active project in this conversation)")
			}
			if userID <= 0 {
				return CreateAppSessionOutput{}, fmt.Errorf("create_web_app_session: user_id is required")
			}

			containerTemplateID, err := parseContainerTemplateID(in.ContainerTemplateID)
			if err != nil {
				return CreateAppSessionOutput{}, fmt.Errorf("create_web_app_session: %w", err)
			}
			if containerTemplateID <= 0 {
				return CreateAppSessionOutput{}, fmt.Errorf("create_web_app_session: container_template_id is required")
			}
			if creator == nil {
				return CreateAppSessionOutput{}, fmt.Errorf("create_web_app_session: app session creator is not available in this runtime")
			}

			result, createErr := creator.CreateWebAppSession(ctx, runtimecontext.WebAppSessionCreateInput{
				UserID:              userID,
				ProjectID:           projectID,
				ContainerTemplateID: containerTemplateID,
				Name:                strings.TrimSpace(in.Name),
			})
			if createErr != nil {
				return CreateAppSessionOutput{
					AppSessionCreateResult: runtimecontext.AppSessionCreateResult{
						ContainerTemplateID: containerTemplateID,
						SessionKind:         "web",
					},
					Error: createErr.Error(),
				}, nil
			}
			return CreateAppSessionOutput{AppSessionCreateResult: result}, nil
		},
	).WithPermission("write", "", "创建 Web 应用会话并拉起容器")
}
