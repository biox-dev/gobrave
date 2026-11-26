package runtimecontext

import "context"

// 本文件定义「按容器模板创建应用会话」端口，供 builtin 的
// create_web_app_session / create_terminal_app_session 使用。
//
// 两类会话是同一个实体（types.AppSession），区别只在 SessionKind：
//   - Web 会话（AppSessionKindWeb）：容器按模板命令启动并暴露服务，前端经网关访问应用；
//     由 interfaces.ContainerService.CreateAppSessionByTemplate 创建；
//   - 终端会话（AppSessionKindTerminal）：容器常驻，交互式 shell 由终端会话注册表打开；
//     由 manager.TerminalSessionRegistry.CreateForTemplate 创建。
//
// 与其它写能力一样，它是进程级静态依赖，由 container 装配后经 builtin.Deps 注入；
// 用户 / 项目 / 发起方会话取自执行事实（tool.UserIDFrom / ProjectIDFrom / SessionIDFrom）。

// AppSessionCreator 抽象「按容器模板创建应用会话（Web / 终端）」的能力。
type AppSessionCreator interface {
	// CreateWebAppSession 创建一条 Web 应用会话（SessionKind=web），并按模板异步拉起容器。
	CreateWebAppSession(ctx context.Context, in WebAppSessionCreateInput) (AppSessionCreateResult, error)
	// CreateTerminalAppSession 创建一条终端应用会话（SessionKind=terminal），
	// 立即拉起常驻容器与交互式 shell。
	CreateTerminalAppSession(ctx context.Context, in TerminalAppSessionCreateInput) (AppSessionCreateResult, error)
}

// WebAppSessionCreateInput 是一次「创建 Web 应用会话」的入参。
type WebAppSessionCreateInput struct {
	// UserID / ProjectID 取自执行事实（tool.UserIDFrom / tool.ProjectIDFrom）。
	UserID    int64
	ProjectID int64
	// ContainerTemplateID 是必填的可运行容器模板 ID（go_container_template_definition 主键）。
	ContainerTemplateID int64
	// Name 是会话展示名；为空时由实现按模板生成默认名。
	Name string
}

// TerminalAppSessionCreateInput 是一次「创建终端应用会话」的入参。
type TerminalAppSessionCreateInput struct {
	// UserID / ProjectID 取自执行事实（tool.UserIDFrom / tool.ProjectIDFrom）。
	UserID    int64
	ProjectID int64
	// ContainerTemplateID 是必填的可运行容器模板 ID（go_container_template_definition 主键）。
	ContainerTemplateID int64
	// Rows / Cols 是新建 PTY 的初始尺寸；为 0 时使用默认 24x80。
	Rows uint16
	Cols uint16
}

// AppSessionCreateResult 是创建应用会话后回传给模型的统一结果。
type AppSessionCreateResult struct {
	// SessionID 是应用会话主键（types.AppSession.ID）；终端会话的 attach / exec 也用它。
	SessionID int64 `json:"session_id,string"`
	// ContainerTemplateID 是本次会话使用的可运行容器模板 ID。
	ContainerTemplateID int64 `json:"container_template_id,string"`
	// SessionKind 取 web / terminal，与 types.AppSessionKind* 一致。
	SessionKind string `json:"session_kind"`
	// Name 是会话展示名（服务端生成或调用方指定）。
	Name string `json:"name,omitempty"`
	// Status 是会话当前状态（如 RUNNING / CREATE_PENDING / CREATING）。
	Status string `json:"status,omitempty"`
	// Persistent 仅对终端会话有意义：true 表示会话不会被一次性回收。
	Persistent bool `json:"persistent,omitempty"`
}
