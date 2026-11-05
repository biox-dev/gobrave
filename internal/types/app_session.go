package types

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
