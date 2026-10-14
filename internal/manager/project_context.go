package manager

import "context"

// ProjectContextProvider 是领域上下文提供者接口的窄定义。
//
// 它描述「按用户解析当前项目上下文文本块」的能力，具体实现见
// AgentProjectContextProvider。当前 Agent 系统提示词的注入调用已被移除，本接口
// 仅作为保留能力定义，供后续按需接入。
type ProjectContextProvider interface {
	// ProjectContext 返回当前用户激活项目下的上下文文本块；无可用内容时返回空串。
	ProjectContext(ctx context.Context, userID string) (string, error)
}
