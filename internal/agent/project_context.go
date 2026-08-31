package agent

import "context"

// ProjectContextProvider 是可选的领域上下文提供者。
//
// AgentService 在调用前通过它把当前项目相关的背景（例如已完成的分析节点）注入
// SystemPrompt，让 Agent「知道」当前项目的进展与可用结果。它保持 agent 包与领域层
// （project / analysis）的解耦：agent 只依赖这个窄接口，具体实现由上层（manager /
// container）注入。
type ProjectContextProvider interface {
	// ProjectContext 返回当前用户激活项目下的上下文文本块；无可用内容时返回空串。
	//
	// 返回的文本会作为独立段落追加到 SystemPrompt 末尾。
	ProjectContext(ctx context.Context, userID string) (string, error)
}
