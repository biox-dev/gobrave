package types

import "strings"

// 本文件保留「容器模板终端会话」在持久层的常量定义。
//
// 历史实现曾用独立的 types.TerminalSession（表 go_terminal_session）记录
// (userID, projectID, templateID) → runtimeID 的映射；现在终端会话与「应用会话」
// 是同一个实体 types.AppSession（表 go_container_app_session），通过
// AgentSessionID / Persistent 两个字段区分 UI 手动创建与 Agent 创建、
// 持久与一次性会话，并用 SessionKind 区分「Web 应用会话」与「终端会话」。
// 容器（ContainerInstance）通过 (owner_type=app_session, owner_id=AppSession.ID)
// 关联，不再单独记录 runtimeID。

// TerminalSessionStatus 是终端会话在持久层的状态。
type TerminalSessionStatus string

const (
	// TerminalSessionStatusRunning 表示会话对应的容器仍然存活。
	TerminalSessionStatusRunning TerminalSessionStatus = "running"
	// TerminalSessionStatusClosed 表示会话已被显式关闭或回收。
	TerminalSessionStatusClosed TerminalSessionStatus = "closed"
)

// AppSession 状态常量（AppSession.Status 是字符串列，这里集中定义便于比较）。
const (
	AppSessionStatusRunning  = "RUNNING"
	AppSessionStatusStopped  = "STOPPED"
	AppSessionStatusFailed   = "FAILED"
	AppSessionStatusCreating = "CREATING"
	AppSessionStatusPending  = "CREATE_PENDING"
)

// AppSession.SessionKind 的取值：区分会话是「Web 应用会话」还是「终端会话」。
//
// 注意与「创建者」区分：创建者是 AgentSessionID（0 = UI，>0 = Agent 会话），
// 生命周期策略是 Persistent（持久会话 / 一次性会话）。
const (
	// AppSessionKindWeb 表示 Web 应用会话：由 ContainerService.CreateAppSessionByTemplate 创建，
	// 容器运行模板命令，前端通过网关路由访问应用页面。
	AppSessionKindWeb = "web"
	// AppSessionKindTerminal 表示终端会话：由终端会话注册表创建（createOrAdopt），
	// 容器以 keep-alive 命令常驻，交互式 shell 由运行时 exec 打开。
	AppSessionKindTerminal = "terminal"
)

// NormalizedSessionKind 返回归一化后的会话种类。
//
// 加列之前的历史行只有迁移回填的值；仍为空时按 AgentSessionID / Persistent 推断
// （终端会话至少满足其一），其余一律视为 Web 应用会话，避免前端展示空标签。
func (a *AppSession) NormalizedSessionKind() string {
	if a == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(a.SessionKind)) {
	case AppSessionKindTerminal:
		return AppSessionKindTerminal
	case AppSessionKindWeb:
		return AppSessionKindWeb
	}
	if a.AgentSessionID > 0 || a.Persistent {
		return AppSessionKindTerminal
	}
	return AppSessionKindWeb
}
