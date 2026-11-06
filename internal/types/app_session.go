package types

// AppSessionEvent 是「会话层 → 实时层」的统一事件。
//
// 会话层（manager.TerminalSessionRegistry）不再自己推 WebSocket 消息，而是把会话
// 生命周期 / 运行态变化发布到 event.Bus，由 manager.AppSessionEventHandler 统一
// 转成前端消息，保证「会话状态推送」只有一个出口：
//   - Action 为空           → action=component.invoke，category=app-session，
//     id=会话 ID，method=Method，args=Args（前端列表据此 refetch）；
//   - Action=terminal.exec  → 直接以 Args 作为 payload 推送，前端据此自动打开终端标签。
type AppSessionEvent struct {
	// AppSessionID 是会话主键（= types.AppSession.ID）。
	AppSessionID int64
	// UserID 是接收推送的用户；0 表示无接收者，handler 直接丢弃。
	UserID    int64
	ProjectID int64
	// ContainerTemplateID 是会话使用的容器模板（终端标签按模板分组时使用）。
	ContainerTemplateID int64
	// Status 是会话最新状态（RUNNING / STOPPED / ...），为空时沿用数据库里的值。
	Status string
	// Method 是前端 component.invoke 的 method：sessionCreated / sessionDeleted /
	// sessionRunning / sessionIdle / sessionAttached / sessionDetached。
	Method string
	// Action 为空表示 component.invoke；其它值（如 terminal.exec）作为 action 直接推送。
	Action string
	// Args 是随事件一起推给前端的参数（busy / subscribers / persistent / command ...）。
	Args map[string]any
}

// TerminalAppSessionInput 是「按容器模板新建终端会话」的入参。
//
// 终端会话就是 AppSession：AgentSessionID 区分 UI 手动创建（0）与 Agent 创建（>0），
// Persistent 决定会话能否在最后一个订阅者离开后继续存活。
type TerminalAppSessionInput struct {
	UserID              int64
	ProjectID           int64
	ContainerTemplateID int64
	AgentSessionID      int64
	Name                string
	WorkspacePath       string
	Persistent          bool
}
