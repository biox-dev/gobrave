package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateTerminalAppSessionToolName 是「按容器模板创建终端应用会话」的工具名。
const CreateTerminalAppSessionToolName = "create_terminal_app_session"

// CreateTerminalAppSessionInput 是 create_terminal_app_session 工具的入参。
//
// UserID / ProjectID 不由模型提供：它们取自执行事实（tool.UserIDFrom / tool.ProjectIDFrom）。
type CreateTerminalAppSessionInput struct {
	// ContainerTemplateID 是可运行的容器模板 ID（支持 "@container:<id>"）。
	ContainerTemplateID string `json:"container_template_id"`
	// Rows / Cols 是前端 xterm 的初始尺寸（可选）；为 0 时使用默认 24x80。
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// CreateTerminalAppSession 返回「按容器模板创建终端应用会话」的工具。
//
// 行为：
//   - 创建一条持久的终端会话（SessionKind=terminal），创建时即拉起常驻容器并打开 shell；
//   - 会话一旦建立即为持久会话：不会被一次性回收，只能由空闲 TTL 或用户显式关闭回收；
//   - 只创建会话、不执行命令；需要执行命令时用 terminal_exec（bind 到同一模板）。
//
// 该工具会创建业务数据并拉起容器，因此声明 write 授权：上层在执行前会先走权限门禁。
func CreateTerminalAppSession(creator runtimecontext.AppSessionCreator) tool.Tool {
	return tool.NewFunc(
		CreateTerminalAppSessionToolName,
		"按容器模板创建一条终端应用会话（SessionKind=terminal）：立即拉起常驻容器并打开交互式 shell，返回 session_id。适用于「为用户准备一个可交互的终端 / 需要跨多次调用保留 cd / export / 已安装包等状态」的场景。只创建会话，不执行命令；要执行命令请接着调用 terminal_exec（同一 container_template_id）。会话为持久会话，用户可在前端接入该终端；不会被一次性回收，只能由空闲超时或用户显式关闭回收。用户与项目取自当前会话上下文，无需传入。",
		tool.Schema("创建终端应用会话的入参", map[string]any{
			"container_template_id": tool.StringProperty("可运行的容器模板 ID（必填，字符串以保精度）；可直接使用用户消息里的 @container:<id>（冒号后的 ID）。"),
			"rows":                  tool.IntegerProperty("PTY 的初始行数（可选），为空时使用默认 24。"),
			"cols":                  tool.IntegerProperty("PTY 的初始列数（可选），为空时使用默认 80。"),
		}, "container_template_id"),
		func(ctx context.Context, in CreateTerminalAppSessionInput) (CreateAppSessionOutput, error) {
			userID := tool.UserIDFrom(ctx)
			projectID := tool.ProjectIDFrom(ctx)
			if projectID <= 0 {
				return CreateAppSessionOutput{}, fmt.Errorf("create_terminal_app_session: project_id is required (no active project in this conversation)")
			}
			if userID <= 0 {
				return CreateAppSessionOutput{}, fmt.Errorf("create_terminal_app_session: user_id is required")
			}

			containerTemplateID, err := parseContainerTemplateID(in.ContainerTemplateID)
			if err != nil {
				return CreateAppSessionOutput{}, fmt.Errorf("create_terminal_app_session: %w", err)
			}
			if containerTemplateID <= 0 {
				return CreateAppSessionOutput{}, fmt.Errorf("create_terminal_app_session: container_template_id is required")
			}
			if creator == nil {
				return CreateAppSessionOutput{}, fmt.Errorf("create_terminal_app_session: app session creator is not available in this runtime")
			}

			rows, err := terminalDimensionToUint16(in.Rows, "rows")
			if err != nil {
				return CreateAppSessionOutput{}, fmt.Errorf("create_terminal_app_session: %w", err)
			}
			cols, err := terminalDimensionToUint16(in.Cols, "cols")
			if err != nil {
				return CreateAppSessionOutput{}, fmt.Errorf("create_terminal_app_session: %w", err)
			}

			result, createErr := creator.CreateTerminalAppSession(ctx, runtimecontext.TerminalAppSessionCreateInput{
				UserID:              userID,
				ProjectID:           projectID,
				ContainerTemplateID: containerTemplateID,
				Rows:                rows,
				Cols:                cols,
			})
			if createErr != nil {
				return CreateAppSessionOutput{
					AppSessionCreateResult: runtimecontext.AppSessionCreateResult{
						ContainerTemplateID: containerTemplateID,
						SessionKind:         "terminal",
					},
					Error: createErr.Error(),
				}, nil
			}
			return CreateAppSessionOutput{AppSessionCreateResult: result}, nil
		},
	).WithPermission("write", "", "创建终端应用会话并拉起容器")
}

// terminalDimensionToUint16 校验 PTY 尺寸：0（缺省）保留为 0 交给实现取默认值，
// 超出 uint16 范围的取值按上限截断，负数报错。
func terminalDimensionToUint16(value int, field string) (uint16, error) {
	if value < 0 {
		return 0, fmt.Errorf("%s must not be negative", field)
	}
	const maxDimension = 1<<16 - 1
	if value > maxDimension {
		return maxDimension, nil
	}
	return uint16(value), nil
}
