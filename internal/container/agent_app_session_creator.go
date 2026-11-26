package container

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// 本文件实现「按容器模板创建应用会话」能力，供 builtin 的
// create_web_app_session / create_terminal_app_session 使用。
//
// 两类会话是同一个实体（types.AppSession），只是创建入口不同：
//   - Web 会话：复用 interfaces.ContainerService.CreateAppSessionByTemplate，
//     容器按模板命令启动并由 outbox 异步创建（Sync=false）；
//   - 终端会话：复用 *manager.TerminalSessionRegistry.CreateForTemplate，
//     创建时即拉起常驻容器并打开交互式 shell（Sync=true）。
//
// 业务校验（模板是否存在、配额、容器创建失败回收记录）全部交给下层服务，
// 这里只做「工具入参 → 服务调用 → 统一结果」的转换，避免两处各写一套规则。

// agentAppSessionCreator 同时实现 runtimecontext.AppSessionCreator。
type agentAppSessionCreator struct {
	containerSvc interfaces.ContainerService
	terminal     *manager.TerminalSessionRegistry
}

// newAgentAppSessionCreator 构造应用会话创建能力。
//
// containerSvc 提供 Web 会话创建；terminal 提供终端会话创建（PTY + 容器）。
func newAgentAppSessionCreator(containerSvc interfaces.ContainerService, terminal *manager.TerminalSessionRegistry) *agentAppSessionCreator {
	return &agentAppSessionCreator{containerSvc: containerSvc, terminal: terminal}
}

// CreateWebAppSession 实现 runtimecontext.AppSessionCreator。
//
// 直接转发到 ContainerService.CreateAppSessionByTemplate：落库 AppSession（SessionKind=web），
// 再按模板异步创建容器。会话状态由服务层维护（CREATE_PENDING → RUNNING / FAILED）。
func (c *agentAppSessionCreator) CreateWebAppSession(ctx context.Context, in runtimecontext.WebAppSessionCreateInput) (runtimecontext.AppSessionCreateResult, error) {
	if c.containerSvc == nil {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_web_app_session: container service is not configured")
	}
	if in.UserID <= 0 {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_web_app_session: user id is required")
	}
	if in.ProjectID <= 0 {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_web_app_session: project id is required")
	}
	if in.ContainerTemplateID <= 0 {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_web_app_session: container template id is required")
	}

	rec, err := c.containerSvc.CreateAppSessionByTemplate(
		ctx, in.UserID, in.ProjectID, in.ContainerTemplateID, strings.TrimSpace(in.Name),
	)
	if err != nil {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_web_app_session: %w", err)
	}
	if rec == nil {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_web_app_session: empty session record")
	}

	return runtimecontext.AppSessionCreateResult{
		SessionID:           rec.ID,
		ContainerTemplateID: rec.ContainerTemplateID,
		SessionKind:         types.AppSessionKindWeb,
		Name:                rec.Name,
		Status:              rec.Status,
	}, nil
}

// CreateTerminalAppSession 实现 runtimecontext.AppSessionCreator。
//
// 直接转发到 TerminalSessionRegistry.CreateForTemplate：创建持久终端会话
// （UI 手动创建同款），并同步拉起常驻容器与交互式 shell。会话建立后用户可从
// 前端接入该终端，Agent 侧也可用 terminal_exec 在同一模板上继续执行命令。
func (c *agentAppSessionCreator) CreateTerminalAppSession(ctx context.Context, in runtimecontext.TerminalAppSessionCreateInput) (runtimecontext.AppSessionCreateResult, error) {
	if c.terminal == nil {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_terminal_app_session: terminal registry is not configured")
	}
	if in.UserID <= 0 {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_terminal_app_session: user id is required")
	}
	if in.ProjectID <= 0 {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_terminal_app_session: project id is required")
	}
	if in.ContainerTemplateID <= 0 {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_terminal_app_session: container template id is required")
	}

	sess, rec, err := c.terminal.CreateForTemplate(ctx, in.UserID, in.ProjectID, in.ContainerTemplateID, in.Rows, in.Cols)
	if err != nil {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_terminal_app_session: %w", err)
	}
	if sess == nil {
		return runtimecontext.AppSessionCreateResult{}, fmt.Errorf("create_terminal_app_session: empty terminal session")
	}

	result := runtimecontext.AppSessionCreateResult{
		SessionID:           sess.Key().ID,
		ContainerTemplateID: in.ContainerTemplateID,
		SessionKind:         types.AppSessionKindTerminal,
		Persistent:          true,
	}
	if rec != nil {
		result.Name = strings.TrimSpace(rec.Name)
		if rec.ID != 0 {
			result.SessionID = rec.ID
		}
	}
	// 运行态以注册表为准（活着的终端会话即 RUNNING；否则回退到记录上的状态）。
	if status := c.terminal.Status(sess.Key()); status.Alive {
		result.Status = types.AppSessionStatusRunning
	} else if rec != nil {
		result.Status = rec.Status
	}
	return result, nil
}
