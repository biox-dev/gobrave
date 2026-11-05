package types

// 本文件保留「容器模板终端会话」在持久层的常量定义。
//
// 历史实现曾用独立的 types.TerminalSession（表 go_terminal_session）记录
// (userID, projectID, templateID) → runtimeID 的映射；现在终端会话与「应用会话」
// 是同一个实体 types.AppSession（表 go_container_app_session），通过
// AgentSessionID / Persistent 两个字段区分 UI 手动创建与 Agent 创建、
// 持久与一次性会话。容器（ContainerInstance）通过
// (owner_type=app_session, owner_id=AppSession.ID) 关联，不再单独记录 runtimeID。

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
